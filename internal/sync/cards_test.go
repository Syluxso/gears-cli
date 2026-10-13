package sync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/card"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
)

// fakeBoard is gears-api's board as far as sync sees it (GRS-47): states
// merge by name, card numbers are assigned (a proposal kept only when free),
// and an API from before GRS-47 has no board route and ignores the tables.
type fakeBoard struct {
	noCards      bool // no board route, card tables ignored on push
	ignoreTables bool // board route answers, card tables ignored on push
	prefix       string
	states       []api.BoardState
	numbers      map[string]int // card id -> number
	used         map[int]bool
}

func (b *fakeBoard) seed() {
	if b.states != nil {
		return
	}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, n := range card.DefaultStates {
		b.states = append(b.states, api.BoardState{ID: project.NewID(), Name: n, Position: i, IsDefault: i == 0,
			IsTerminal: i == len(card.DefaultStates)-1, CreatedAt: at, UpdatedAt: at})
	}
}

func (b *fakeBoard) get() api.Board {
	b.seed()
	return api.Board{Prefix: b.prefix, States: b.states}
}

func (b *fakeBoard) take(id string, n int) {
	if b.numbers == nil {
		b.numbers, b.used = map[string]int{}, map[int]bool{}
	}
	b.numbers[id], b.used[n] = n, true
}

func (b *fakeBoard) push(tbl string, m map[string]any) api.PushResult {
	id := m["id"].(string)
	res := api.PushResult{Table: tbl, ID: id, Applied: true}
	switch tbl {
	case "card_states":
		b.seed()
		name, _ := m["name"].(string)
		for _, s := range b.states {
			if s.ID == id {
				return res
			}
			if strings.EqualFold(s.Name, name) {
				res.Applied, res.Assigned = false, &api.Assigned{ID: s.ID}
				return res
			}
		}
		b.states = append(b.states, api.BoardState{ID: id, Name: name})
	case "cards":
		n, ok := b.numbers[id]
		if !ok {
			want := int(m["number"].(float64))
			n = want
			if want <= 0 || b.used[want] {
				n = 0
				for u := range b.used {
					n = max(n, u)
				}
				n++
			}
			b.take(id, n)
		}
		pre := b.prefix
		if pre == "" {
			pre = "WS"
		}
		res.Assigned = &api.Assigned{Number: n, Key: pre + "-" + strconv.Itoa(n)}
	}
	return res
}

func newTask(t *testing.T, p *project.Project, title string) *card.Card {
	t.Helper()
	c, err := card.New(p, card.Input{Type: "task", Title: title, CardData: map[string]any{
		"task": map[string]any{"what": title, "soThat": "it is tested"}, "acceptance": []any{"it syncs, proven by the test"}}}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func rootProject(t *testing.T) *project.Project {
	t.Helper()
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	return p
}

func rowsOf(in api.PushIn, tbl string) []map[string]any {
	var out []map[string]any
	for _, r := range in.Rows[tbl] {
		out = append(out, r.(map[string]any))
	}
	return out
}

// Cards made offline go up with their trail in the API's order; the API's
// state ids replace the seeded ones, its numbers replace the proposals (a
// number another machine took moves on), and none-times go as null.
func TestSyncCardsPushAdoptsNumbersAndStates(t *testing.T) {
	p := rootProject(t)
	a := newTask(t, p, "Alpha")
	b := newTask(t, p, "Beta")
	if a.Key != "WS-1" || b.Key != "WS-2" {
		t.Fatalf("offline keys: %s %s", a.Key, b.Key)
	}
	if _, err := card.Tag(p, a.Key, []string{"api"}, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := card.AddComment(p, a.Key, "found it", "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := card.AddChecklist(p, a.Key, "UAT", []string{"works"}, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := card.Tick(p, a.Key, "UAT", "1", true, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := card.AddLink(p, a.Key, "blocks", b.Key, "", "", "tester"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := card.Move(p, b.Key, "In Progress", "", "tester"); err != nil {
		t.Fatal(err)
	}

	f := &fakeAPI{}
	f.board.take("01a1ffff-0000-7000-8000-000000000001", 1) // another machine's card holds 1
	srv := httptest.NewServer(f)
	defer srv.Close()
	r, err := Run(context.Background(), api.Anonymous(srv.URL), p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cards != "synced" || r.Failed != 0 || r.CardsRenumbered != 2 {
		t.Fatalf("report: %+v", r)
	}

	// States: the API's ids, nothing resent.
	in := f.pushes[0]
	if n := len(in.Rows["card_states"]); n != 0 {
		t.Fatalf("seeded states pushed: %d", n)
	}
	sts, err := card.States(p)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, s := range f.board.states {
		ids[s.Name] = s.ID
	}
	for _, s := range sts {
		if ids[s.Name] != s.ID {
			t.Fatalf("state %s kept local id %s (api %s)", s.Name, s.ID, ids[s.Name])
		}
	}

	// Cards: proposal, tags, nulls, state ids.
	cards := rowsOf(in, "cards")
	if len(cards) != 2 {
		t.Fatalf("card rows: %v", cards)
	}
	for _, c := range cards {
		for _, k := range []string{"dueAt", "parentId", "completedAt"} {
			if v, ok := c[k]; !ok || v != nil {
				t.Fatalf("%s: %v (want null)", k, v)
			}
		}
		if c["id"] == a.ID {
			if tags, _ := c["tags"].([]any); len(tags) != 1 || tags[0] != "api" {
				t.Fatalf("tags: %v", c["tags"])
			}
			if c["number"].(float64) != 1 || c["stateId"] != ids["Backlog"] {
				t.Fatalf("alpha row: %v", c)
			}
		} else if c["stateId"] != ids["In Progress"] || c["updatedBy"] != "tester" {
			t.Fatalf("beta row: %v", c)
		}
	}
	if l := rowsOf(in, "card_links"); len(l) != 1 || l[0]["targetCardId"] != b.ID || l[0]["kind"] != "blocks" {
		t.Fatalf("links: %v", l)
	}
	if it := rowsOf(in, "card_checklist_items"); len(it) != 1 || it[0]["done"] != true || it[0]["doneAt"] == nil || it[0]["checklistId"] == "" {
		t.Fatalf("items: %v", it)
	}
	if n := len(rowsOf(in, "card_comments")); n != 1 {
		t.Fatalf("comments: %d", n)
	}
	evs := rowsOf(in, "card_events")
	if len(evs) < 6 {
		t.Fatalf("events: %v", evs)
	}
	for _, e := range evs {
		if id, _ := e["id"].(string); len(id) != 36 || id[14] != '7' {
			t.Fatalf("event id not a UUID v7: %v", e["id"])
		}
	}

	// Numbers: 1 was taken, so alpha is 2 and beta (proposed 2) is 3.
	if c, err := card.Get(p, "WS-2"); err != nil || c.ID != a.ID {
		t.Fatalf("WS-2: %+v %v", c, err)
	}
	if c, err := card.Get(p, "WS-3"); err != nil || c.ID != b.ID || c.State != "In Progress" {
		t.Fatalf("WS-3: %+v %v", c, err)
	}
	if next := newTask(t, p, "Gamma"); next.Key != "WS-4" {
		t.Fatalf("next proposal: %s", next.Key)
	}
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if since, _ := stateDB(db, "cards_pushed_at"); since == "" {
		t.Fatal("card watermark not set")
	}
}

// Pulled board rows land in order whatever order they arrive in; a local
// card holding a number the API gave another card moves on; pulled deletes
// soft-delete cards and drop links and checklists.
func TestSyncCardsPull(t *testing.T) {
	p := rootProject(t)
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	ctx := context.Background()
	if _, err := Run(ctx, cl, p); err != nil {
		t.Fatal(err)
	}
	local := newTask(t, p, "Local")
	if _, err := Run(ctx, cl, p); err != nil {
		t.Fatal(err)
	}

	const (
		review = "01a1aaaa-0000-7000-8000-000000000001"
		xid    = "01a1aaaa-0000-7000-8000-000000000002"
		clid   = "01a1aaaa-0000-7000-8000-000000000003"
		lid    = "01a1aaaa-0000-7000-8000-000000000004"
	)
	at := "2026-10-12T10:00:00Z"
	row := func(tbl, id string, v map[string]any) api.Change {
		v["id"] = id
		b, _ := json.Marshal(v)
		return api.Change{Table: tbl, ID: id, Op: "upsert", Row: b}
	}
	f.queue([]api.Change{
		row("card_events", "01a1aaaa-0000-7000-8000-000000000005", map[string]any{"cardId": xid, "at": at, "agent": "elsewhere", "kind": "created", "detail": "task in Review"}),
		row("card_checklist_items", "01a1aaaa-0000-7000-8000-000000000006", map[string]any{"checklistId": clid, "title": "ships", "done": true, "position": 0,
			"doneAt": at, "doneBy": "elsewhere", "createdAt": at, "updatedAt": at}),
		row("card_comments", "01a1aaaa-0000-7000-8000-000000000007", map[string]any{"cardId": xid, "body": "from elsewhere", "agent": "elsewhere", "createdAt": at, "updatedAt": at}),
		row("card_links", lid, map[string]any{"cardId": xid, "kind": "related", "targetCardId": local.ID, "url": "", "title": "", "agent": "elsewhere", "createdAt": at, "updatedAt": at}),
		row("card_checklists", clid, map[string]any{"cardId": xid, "title": "UAT", "position": 0, "createdAt": at, "updatedAt": at}),
		row("cards", xid, map[string]any{"number": 7, "key": "WS-7", "type": "task", "title": "From elsewhere", "body": "",
			"cardData": map[string]any{"task": map[string]any{"what": "x", "soThat": "y"}, "acceptance": []string{"z"}},
			"stateId": review, "state": "Review", "size": "S", "priority": 2, "position": 1, "assigneeLabel": "elsewhere", "parentId": nil,
			"dueAt": "2026-11-01T00:00:00Z", "estimateMinutes": 30, "artifact": "", "externalKey": "", "tags": []string{"ops"},
			"createdBy": "elsewhere", "updatedBy": "elsewhere", "createdAt": at, "updatedAt": at, "completedAt": nil}),
		row("card_states", review, map[string]any{"name": "Review", "position": 2, "isDefault": false, "isTerminal": false, "wipLimit": 0, "createdAt": at, "updatedAt": at}),
	})
	r, err := Run(ctx, cl, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Applied != 7 {
		t.Fatalf("applied %d of 7: %v", r.Applied, r.Notes)
	}
	full, err := card.Show(p, "WS-7")
	if err != nil {
		t.Fatal(err)
	}
	if full.ID != xid || full.State != "Review" || full.Size != "S" || full.Priority != 2 || full.Estimate != 30 || full.DueAt.IsZero() ||
		!full.CompletedAt.IsZero() || len(full.Tags) != 1 || full.Tags[0] != "ops" {
		t.Fatalf("pulled card: %+v", full.Card)
	}
	if len(full.Comments) != 1 || len(full.Links) != 1 || full.Links[0].Target != local.Key || len(full.Checklists) != 1 ||
		len(full.Checklists[0].Items) != 1 || !full.Checklists[0].Items[0].Done || len(full.Events) != 1 {
		t.Fatalf("pulled trail: %+v", full)
	}
	// Repeat pulls are no-ops (activity is append-only).
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	ch := row("card_events", "01a1aaaa-0000-7000-8000-000000000005", map[string]any{"cardId": xid, "at": at, "agent": "elsewhere", "kind": "created", "detail": "again"})
	if ok, err := applyCard(db, ch); ok || err != nil {
		t.Fatalf("event re-applied: %v %v", ok, err)
	}

	// A local card made offline proposed 8; the API gave 8 to another card.
	mine := newTask(t, p, "Mine")
	if mine.Key != "WS-8" {
		t.Fatalf("proposal: %s", mine.Key)
	}
	other := row("cards", "01a1aaaa-0000-7000-8000-000000000008", map[string]any{"number": 8, "key": "WS-8", "type": "task", "title": "Theirs",
		"cardData": map[string]any{}, "stateId": "", "state": "Backlog", "createdAt": at, "updatedAt": at})
	if ok, err := applyCard(db, other); !ok || err != nil {
		t.Fatalf("apply: %v %v", ok, err)
	}
	db.Close()
	if c, err := card.Get(p, "WS-8"); err != nil || c.Title != "Theirs" || c.State != "Backlog" {
		t.Fatalf("WS-8: %+v %v", c, err)
	}
	if c, err := card.Get(p, mine.ID); err != nil || c.Key != "WS-9" {
		t.Fatalf("mine moved to: %+v %v", c, err)
	}

	// Deletes from elsewhere.
	f.queue([]api.Change{
		{Table: "cards", ID: xid, Op: "delete", Deleted: true},
		{Table: "card_checklists", ID: clid, Op: "delete", Deleted: true},
		{Table: "card_links", ID: lid, Op: "delete", Deleted: true},
	})
	if r, err = Run(ctx, cl, p); err != nil {
		t.Fatal(err)
	}
	if _, err := card.Get(p, "WS-7"); err == nil {
		t.Fatal("pulled card delete left the card live")
	}
	db, _ = store.Open(p)
	defer db.Close()
	var lists, links, deleted int
	_ = db.QueryRow(`SELECT COUNT(*) FROM card_checklists`).Scan(&lists)
	_ = db.QueryRow(`SELECT COUNT(*) FROM card_links`).Scan(&links)
	_ = db.QueryRow(`SELECT COUNT(*) FROM cards WHERE id = ? AND deleted_at <> ''`, xid).Scan(&deleted)
	if lists != 0 || links != 0 || deleted != 1 {
		t.Fatalf("after pulled deletes: lists %d links %d soft-deleted %d", lists, links, deleted)
	}
}

// Local deletes go up: a card (soft) as its row and a delete, a removed
// link and checklist as tombstones, cleared once the API takes them.
func TestSyncCardsDeletesGoUp(t *testing.T) {
	p := rootProject(t)
	a := newTask(t, p, "Keep")
	b := newTask(t, p, "Drop")
	l, err := card.AddLink(p, a.Key, "related", b.Key, "", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := card.AddChecklist(p, a.Key, "UAT", []string{"one"}, "tester"); err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	if err := card.RemoveLink(p, a.Key, l.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := card.RemoveChecklist(p, a.Key, "UAT", "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := card.Delete(p, b.Key, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}
	in := f.pushes[len(f.pushes)-1]
	if d := in.Deletes; len(d["cards"]) != 1 || d["cards"][0] != b.ID || len(d["card_links"]) != 1 || d["card_links"][0] != l.ID || len(d["card_checklists"]) != 1 {
		t.Fatalf("deletes: %v", in.Deletes)
	}
	found := false
	for _, c := range rowsOf(in, "cards") {
		found = found || c["id"] == b.ID
	}
	if !found {
		t.Fatal("deleted card's row (carrying its trail) not pushed")
	}
	db, _ := store.Open(p)
	defer db.Close()
	var tombs int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_deletes`).Scan(&tombs)
	if tombs != 0 {
		t.Fatalf("tombstones left: %d", tombs)
	}
}

// An API from before GRS-47 (no board route, card tables ignored): sync
// still moves desk, sessions and artifacts, says once why cards stayed, and
// keeps the cards (and their tombstones) for the first sync against an API
// that has the tables. The same holds for an API with a board route that
// ignores the tables on push.
func TestSyncCardsDegradeWithoutCardTables(t *testing.T) {
	for _, mode := range []string{"no board route", "tables ignored"} {
		t.Run(mode, func(t *testing.T) {
			p := rootProject(t)
			a := newTask(t, p, "Waits")
			b := newTask(t, p, "Other")
			l, err := card.AddLink(p, a.Key, "related", b.Key, "", "", "tester")
			if err != nil {
				t.Fatal(err)
			}
			if err := card.RemoveLink(p, a.Key, l.ID, "tester"); err != nil {
				t.Fatal(err)
			}
			e, err := session.Append(p, "done", "tester", "still syncs")
			if err != nil {
				t.Fatal(err)
			}
			f := &fakeAPI{}
			if mode == "no board route" {
				f.board.noCards = true
			} else {
				f.board.ignoreTables = true
				f.board.noCards = false
			}
			srv := httptest.NewServer(f)
			defer srv.Close()
			cl := api.Anonymous(srv.URL)
			r, err := Run(context.Background(), cl, p)
			if err != nil {
				t.Fatal(err)
			}
			notes := strings.Join(r.Notes, "\n")
			if r.Cards != "skipped" || strings.Count(notes, "api has no card tables yet; deploy gears-api") != 1 || r.Failed != 0 {
				t.Fatalf("report: %+v", r)
			}
			last := f.pushes[len(f.pushes)-1]
			if rows := rowsOf(last, "session_entries"); len(rows) != 1 || rows[0]["id"] != e.ID {
				t.Fatalf("session entry not pushed: %v", rows)
			}
			if mode == "no board route" {
				for _, tbl := range cardTables {
					if len(last.Rows[tbl]) > 0 || len(last.Deletes[tbl]) > 0 {
						t.Fatalf("%s sent to an API without card tables", tbl)
					}
				}
			}
			db, _ := store.Open(p)
			since, _ := stateDB(db, "cards_pushed_at")
			var tombs int
			_ = db.QueryRow(`SELECT COUNT(*) FROM sync_deletes WHERE tbl = 'card_links'`).Scan(&tombs)
			db.Close()
			if since != "" || (mode == "no board route" && tombs != 1) {
				t.Fatalf("watermark %q tombstones %d", since, tombs)
			}
			if c, _ := card.Get(p, a.ID); c.Key != "WS-1" {
				t.Fatalf("key changed while skipped: %s", c.Key)
			}

			// The API is deployed: the cards (made before any earlier push
			// watermark) and the tombstone go up.
			f.mu.Lock()
			f.board.noCards, f.board.ignoreTables = false, false
			f.mu.Unlock()
			r, err = Run(context.Background(), cl, p)
			if err != nil {
				t.Fatal(err)
			}
			last = f.pushes[len(f.pushes)-1]
			if r.Cards != "synced" || len(rowsOf(last, "cards")) != 2 {
				t.Fatalf("after deploy: %+v rows %v", r, last.Rows["cards"])
			}
			if mode == "no board route" && len(last.Deletes["card_links"]) != 1 {
				t.Fatalf("tombstone not sent after deploy: %v", last.Deletes)
			}
		})
	}
}
