package sync

// Cards sync immediately (GRS-51). Unlike desk, sessions and artifacts,
// which wait for gears sync, the board moves both ways at once:
//
//   - Up, write-through: every card write (CLI or MCP) pushes the board's
//     pending rows straight away (PushCards), the same push gears sync
//     makes, card tables only. The API's numbers and state ids are applied
//     before the write returns, so gears card new prints the API's key.
//     Offline, the write stands locally and stays queued under the
//     cards_pushed_at watermark and the tombstones, as before.
//   - Down, the board stream: gears run and gears watch hold
//     GET /workspaces/:ws/board/events open (Live) and, a moment after each
//     event, pull the card tables only (PullCards) and apply them.
//   - Backstop: card reads pull first, with a short timeout (CardsBackstop).
//
// Card-only pulls keep their own cursor, cards_cursor: the API's filtered
// cursor skips every non-card change below it, so it must never become the
// main cursor. The main sync still pulls everything with the main cursor and
// then raises cards_cursor to it. A change seen by both is applied twice,
// which is harmless (last write wins by updatedAt).
//
// Locking: one lock per process serialises the local half of card pushes
// and pulls (the MCP server runs tool calls and the stream at once), never
// their HTTP request; a whole sync (Run: gears sync, the sync tool, the
// periodic sync of gears run --sync-interval, GRS-25) holds it from start to
// end, so two syncs never overlap and a card push or pull waits for it,
// bounded by its own timeout. Across processes the cache's busy_timeout and
// immediate transactions make writers wait instead of failing.

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

// cardsMu serialises the local half of card pushes and pulls within one
// process (reading pending rows, applying results), and every whole sync.
// Waiting for it honours the caller's context: a card write's 3s budget
// covers the wait too.
var cardsMu = make(procLock, 1)

// procLock is a mutex whose Lock gives up when ctx ends.
type procLock chan struct{}

func (l procLock) lock(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	default:
	}
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w (%w)", ErrBusy, ctx.Err())
	}
}

func (l procLock) unlock() { <-l }

// ErrBusy means a sync in this process held the lock longer than the
// caller would wait. The caller's rows stay queued for the next push.
var ErrBusy = errors.New("a sync is running")

// ErrCardsNotReady means this cache has never synced its board: the first
// gears sync adopts the API's states and prefix and pulls the board whole.
var ErrCardsNotReady = errors.New("cards have not synced here yet; run gears sync")

// Disabled reports GEARS_CARD_SYNC=off: no write-through, no backstop pull,
// no board stream. Cards then wait for gears sync, as before GRS-51.
func Disabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GEARS_CARD_SYNC")))
	return v == "off" || v == "0" || v == "false"
}

func cardsReady(db *sql.DB) bool {
	v, _ := stateDB(db, "cards_scope")
	return v == "workspace"
}

// raiseCardsCursor moves cards_cursor up to n, never down.
func raiseCardsCursor(db *sql.DB, n int64) error {
	cur, _ := stateDB(db, "cards_cursor")
	have, _ := strconv.ParseInt(cur, 10, 64)
	if cur != "" && have >= n {
		return nil
	}
	return setState(db, "cards_cursor", strconv.FormatInt(n, 10))
}

// ------------------------------------------------------------------- up

// CardPush is what one write-through push did.
type CardPush struct {
	Sent       int      `json:"sent"`
	Applied    int      `json:"applied"`
	Failed     int      `json:"failed"`
	Renumbered int      `json:"renumbered"`
	Notes      []string `json:"notes,omitempty"`
}

// PushCards pushes the board's pending rows (everything changed since
// cards_pushed_at, and card tombstones) and applies the API's answer:
// numbers, keys, merged state ids. No board GET: the cache already holds
// the API's states and prefix from its first sync.
func PushCards(ctx context.Context, c *api.Client, p *project.Project) (*CardPush, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if !cardsReady(db) {
		return nil, ErrCardsNotReady
	}
	ws := workspaceOf(p)
	// The mutex covers reading and applying, never the request: a slow API
	// must not hold up another tool call. Two pushes of the same rows are
	// harmless (last write wins; a card keeps the number it was given).
	if err := cardsMu.lock(ctx); err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			cardsMu.unlock()
		}
	}()
	cs := &cardSync{ok: true, prefix: strings.ToUpper(strings.TrimSpace(p.Prefix)), inSync: map[string]bool{}, now: store.Now()}
	cs.since, _ = stateDB(db, "cards_pushed_at")
	in := api.PushIn{Rows: map[string][]any{}}

	var tombs []struct{ tbl, id string }
	err = scanAll(db, `SELECT tbl, id FROM sync_deletes`, nil, func(sc func(...any) error) error {
		var tbl, id string
		if err := sc(&tbl, &id); err != nil {
			return err
		}
		if isCardTable(tbl) {
			tombs = append(tombs, struct{ tbl, id string }{tbl, id})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, t := range tombs {
		if in.Deletes == nil {
			in.Deletes = map[string][]string{}
		}
		in.Deletes[t.tbl] = append(in.Deletes[t.tbl], t.id)
	}
	if err := collectCards(db, cs, &in); err != nil {
		return nil, fmt.Errorf("cards: %w", err)
	}
	out := &CardPush{Sent: cs.sent + len(tombs)}
	if out.Sent == 0 {
		return out, nil
	}
	cardsMu.unlock()
	locked = false
	res, err := c.Push(ctx, ws, in)
	if err != nil {
		return nil, err
	}
	if err := cardsMu.lock(ctx); err != nil {
		return nil, err
	}
	locked = true
	r := &Report{}
	answered, err := applyCardResults(db, cs, res.Results, r)
	if err != nil {
		return nil, fmt.Errorf("cards: %w", err)
	}
	if !answered {
		return nil, api.ErrNoCards // the watermark stays; gears sync says the same
	}
	failed := map[string]bool{}
	for _, x := range res.Results {
		if !isCardTable(x.Table) {
			continue
		}
		switch {
		case x.Error != "":
			out.Failed++
			failed[x.Table+"/"+x.ID] = true
			out.Notes = append(out.Notes, fmt.Sprintf("push %s %s: %s", x.Table, short(x.ID), x.Error))
		case x.Applied:
			out.Applied++
		}
	}
	for _, t := range tombs {
		if !failed[t.tbl+"/"+t.id] {
			_, _ = db.Exec(`DELETE FROM sync_deletes WHERE tbl = ? AND id = ?`, t.tbl, t.id)
		}
	}
	out.Renumbered = r.CardsRenumbered
	out.Notes = append(out.Notes, r.Notes...)
	return out, setState(db, "cards_pushed_at", cs.now)
}

// CardsUp is the write-through every card verb calls after a local write.
// It never fails the write: pushed is false and note says why when the
// rows stay queued (not logged in, offline, the API refused). Both are
// zero when card sync is off.
func CardsUp(p *project.Project, timeout time.Duration) (pushed bool, note string) {
	if Disabled() {
		return false, ""
	}
	host := api.Host()
	c, err := api.Open(host)
	if err != nil {
		if errors.Is(err, api.ErrNotLoggedIn) {
			return false, "saved locally; not pushed (not logged in to " + hostName(host) + ")"
		}
		return false, "saved locally; not pushed (" + err.Error() + ")"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := PushCards(ctx, c, p)
	if err != nil {
		return false, "saved locally; not pushed (" + shortErr(err, host) + "); gears sync will push it"
	}
	if res.Failed > 0 {
		return true, strings.Join(res.Notes, "; ")
	}
	return true, ""
}

// ----------------------------------------------------------------- down

// CardChange is one applied change from a card pull, for gears watch.
type CardChange struct {
	At    time.Time `json:"at"`
	Table string    `json:"table"`
	Key   string    `json:"key,omitempty"`
	What  string    `json:"what"`
	By    string    `json:"by,omitempty"`
}

// CardPull is what one card pull did. Fallback is set when the API ignored
// the tables filter and a full pull (main cursor) ran instead.
type CardPull struct {
	Pulled   int          `json:"pulled"`
	Applied  int          `json:"applied"`
	Cursor   int64        `json:"cursor"`
	Fallback bool         `json:"fallback,omitempty"`
	Changes  []CardChange `json:"changes,omitempty"`
}

// PullCards pulls the card tables after cards_cursor and applies them.
func PullCards(ctx context.Context, c *api.Client, p *project.Project) (*CardPull, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if !cardsReady(db) {
		return nil, ErrCardsNotReady
	}
	ws := workspaceOf(p)
	cur := ""
	if cur, _ = stateDB(db, "cards_cursor"); cur == "" {
		cur, _ = stateDB(db, "cursor") // everything up to it is applied
	}
	cursor, _ := strconv.ParseInt(cur, 10, 64)
	out := &CardPull{Cursor: cursor}
	for {
		page, err := c.PullTables(ctx, ws, cursor, "cards")
		if err != nil {
			return out, fmt.Errorf("pull: %w", err)
		}
		if page.Tables == nil {
			// An API from before the filter sent everything after a cursor
			// it read as the main one. Do the main pull instead.
			r := &Report{Notes: []string{}}
			if err := pull(ctx, c, p, db, ws, r, &cardSync{ok: true, inSync: map[string]bool{}}); err != nil {
				return out, err
			}
			out.Fallback, out.Pulled, out.Applied, out.Cursor = true, r.Pulled, r.Applied, r.Cursor
			return out, nil
		}
		// Apply under the mutex (never the request): two pulls racing from
		// the same cursor apply the same rows twice, which changes nothing.
		if err := cardsMu.lock(ctx); err != nil {
			return out, err
		}
		sortForApply(page.Changes)
		for _, ch := range page.Changes {
			if !isCardTable(ch.Table) {
				continue
			}
			out.Pulled++
			cc, applied, err := applyDescribed(db, ch)
			if err != nil {
				continue // left for the next full sync, which reports it
			}
			if applied {
				out.Applied++
			}
			if cc != nil {
				out.Changes = append(out.Changes, *cc)
			}
		}
		if page.Cursor > cursor {
			cursor = page.Cursor
		}
		err = raiseCardsCursor(db, cursor)
		cardsMu.unlock()
		if err != nil {
			return out, err
		}
		out.Cursor = cursor
		if !page.More || len(page.Changes) == 0 {
			return out, nil
		}
	}
}

// CardsReady reports whether this cache has synced its board once (the
// first gears sync with cards), which card-only push and pull need.
func CardsReady(p *project.Project) bool {
	if !store.Exists(p) {
		return false
	}
	db, err := store.Open(p)
	if err != nil {
		return false
	}
	defer db.Close()
	return cardsReady(db)
}

// cardSnap is what a change looks like from here, before and after.
type cardSnap struct {
	exists                        bool
	key, state, updated, closedAt string
}

func snapCard(db *sql.DB, id string) cardSnap {
	var s cardSnap
	err := db.QueryRow(`SELECT c.key, COALESCE(s.name, ''), c.updated_at, c.closed_at FROM cards c LEFT JOIN card_states s ON s.id = c.state_id
		WHERE c.id = ? AND c.deleted_at = ''`, id).Scan(&s.key, &s.state, &s.updated, &s.closedAt)
	s.exists = err == nil
	return s
}

// childStamp is a child row's updated_at here ("" when absent) and its card.
func childStamp(db *sql.DB, tbl, id string) (updated, cardID string) {
	switch tbl {
	case "card_comments", "card_links", "card_checklists":
		_ = db.QueryRow(`SELECT updated_at, card_id FROM `+tbl+` WHERE id = ?`, id).Scan(&updated, &cardID)
	case "card_checklist_items":
		_ = db.QueryRow(`SELECT i.updated_at, l.card_id FROM card_checklist_items i JOIN card_checklists l ON l.id = i.checklist_id WHERE i.id = ?`, id).Scan(&updated, &cardID)
	case "card_events":
		_ = db.QueryRow(`SELECT at, card_id FROM card_events WHERE uid = ?`, id).Scan(&updated, &cardID)
	}
	return updated, cardID
}

func keyOf(db *sql.DB, cardID string) string {
	var k string
	_ = db.QueryRow(`SELECT key FROM cards WHERE id = ?`, cardID).Scan(&k)
	return k
}

// applyDescribed applies one pulled card change and says what it changed
// here, or nil when nothing visible did (an echo of this machine's own push,
// an activity line). Last write wins as in the full pull: a pulled row
// stamped the same second as the local copy is applied (the API is the
// authority on a tie).
func applyDescribed(db *sql.DB, ch api.Change) (*CardChange, bool, error) {
	cc := &CardChange{At: ch.At, Table: ch.Table, By: ch.By}
	switch ch.Table {
	case "cards":
		before := snapCard(db, ch.ID)
		applied, err := applyCard(db, ch)
		if err != nil || !applied {
			return nil, applied, err
		}
		after := snapCard(db, ch.ID)
		cc.Key = after.key
		switch {
		case ch.Deleted:
			cc.Key, cc.What = before.key, "deleted"
		case !before.exists:
			cc.What = "created"
			if after.state != "" {
				cc.What += " in " + after.state
			}
		case before.updated == after.updated && before.state == after.state && before.closedAt == after.closedAt && before.key == after.key:
			return nil, applied, nil
		case before.state != after.state:
			cc.What = "moved → " + after.state
		case before.closedAt == "" && after.closedAt != "":
			cc.What = "closed"
		case before.closedAt != "" && after.closedAt == "":
			cc.What = "reopened"
		default:
			cc.What = "updated"
		}
		return cc, applied, nil
	case "card_states":
		applied, err := applyCard(db, ch)
		if err != nil || !applied {
			return nil, applied, err
		}
		var name string
		if !ch.Deleted {
			var s struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(ch.Row, &s)
			name = s.Name
		}
		cc.What = "board state " + strings.TrimSpace(name+" changed")
		if ch.Deleted {
			cc.What = "board state removed"
		}
		return cc, applied, nil
	}
	// Children: compare this cache's stamp with the pulled row's.
	was, cardID := childStamp(db, ch.Table, ch.ID)
	var row struct {
		CardID      string    `json:"cardId"`
		ChecklistID string    `json:"checklistId"`
		UpdatedAt   time.Time `json:"updatedAt"`
	}
	if !ch.Deleted {
		_ = json.Unmarshal(ch.Row, &row)
		if row.CardID != "" {
			cardID = row.CardID
		} else if row.ChecklistID != "" && cardID == "" {
			_ = db.QueryRow(`SELECT card_id FROM card_checklists WHERE id = ?`, row.ChecklistID).Scan(&cardID)
		}
	}
	applied, err := applyCard(db, ch)
	if err != nil || !applied {
		return nil, applied, err
	}
	if ch.Table == "card_events" || (!ch.Deleted && was != "" && was == stamp(row.UpdatedAt)) {
		return nil, applied, nil // activity rides with its change; an echo is no change
	}
	cc.Key = keyOf(db, cardID)
	verbs := map[string][3]string{ // new, edited, deleted
		"card_comments":        {"commented", "comment edited", "comment deleted"},
		"card_links":           {"linked", "link changed", "link removed"},
		"card_checklists":      {"checklist added", "checklist changed", "checklist removed"},
		"card_checklist_items": {"checklist item added", "checklist item changed", "checklist item removed"},
	}[ch.Table]
	switch {
	case ch.Deleted:
		cc.What = verbs[2]
	case was == "":
		cc.What = verbs[0]
	default:
		cc.What = verbs[1]
	}
	return cc, applied, nil
}

// String is one line for gears watch: "07:12 GRS-3 moved → Testing (by browser)".
func (c CardChange) String() string {
	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	s := at.Local().Format("15:04") + " "
	if c.Key != "" {
		s += c.Key + " "
	}
	s += c.What
	if c.By != "" {
		s += " (by " + c.By + ")"
	}
	return s
}

// liveConnected is set while this process holds the board stream open: the
// cache is then fresh to within a moment and reads skip the backstop pull.
var liveConnected atomic.Bool

// LiveConnected reports whether this process's board stream is open.
func LiveConnected() bool { return liveConnected.Load() }

// CardsBackstop is the pull a card read makes first (list, show, status).
// stale is true when the API should have answered and did not in time;
// the read then uses the cache as it is. Not logged in, not synced yet, or
// card sync off: nothing to be stale against.
func CardsBackstop(p *project.Project, timeout time.Duration) (stale bool) {
	if Disabled() || LiveConnected() {
		return false
	}
	c, err := api.Open(api.Host())
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err = PullCards(ctx, c, p)
	return err != nil && !errors.Is(err, ErrCardsNotReady)
}

// --------------------------------------------------------------- stream

// LiveOptions configure the board stream.
type LiveOptions struct {
	Client  *api.Client
	Project *project.Project
	// Logf reports connection state, on stderr only: gears run's stdout is
	// the MCP channel.
	Logf func(format string, args ...any)
	// OnChange receives each applied change (gears watch prints them).
	OnChange func(CardChange)
	// OnPull receives every pull's result or error.
	OnPull func(*CardPull, error)
	// Debounce coalesces a burst of events into one pull (default 300ms).
	Debounce time.Duration
	// Backoff is the first reconnect wait, doubling to MaxBackoff (3s, 5m).
	Backoff, MaxBackoff time.Duration
	// Idle drops a stream that has said nothing, not even a ping, for this
	// long (default 75s; the API pings every 25s).
	Idle time.Duration
	// PullTimeout bounds one pull (default 20s).
	PullTimeout time.Duration
	// Hello says who holds the stream (GRS-37 presence): the agent label
	// and gears run's auto-sync interval.
	Hello api.StreamHello
}

// Live holds the board stream open until ctx ends, pulling the card tables
// after each burst of events and once on every (re)connect. A transient
// failure reconnects with backoff; an answer that will not change (401,
// 403, 404, 405, 501: an API without the live board, a refused token)
// stops it, logged once, and Live returns that *api.StreamError.
func Live(ctx context.Context, o LiveOptions) error {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	// Say a thing once: a repeated message (the same pull failure on every
	// event, the same drop on every retry) is logged only when it changes.
	logf, last := o.Logf, ""
	o.Logf = func(format string, args ...any) {
		if m := fmt.Sprintf(format, args...); m != last {
			last = m
			logf("%s", m)
		}
	}
	if o.Debounce <= 0 {
		o.Debounce = 300 * time.Millisecond
	}
	if o.Backoff <= 0 {
		o.Backoff = 3 * time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 5 * time.Minute
	}
	if o.Idle <= 0 {
		o.Idle = 75 * time.Second
	}
	if o.PullTimeout <= 0 {
		o.PullTimeout = 20 * time.Second
	}
	ws := workspaceOf(o.Project)
	wait := o.Backoff
	for {
		connected, err := liveSession(ctx, o, ws)
		if ctx.Err() != nil {
			return nil
		}
		var se *api.StreamError
		if errors.As(err, &se) && se.Permanent() {
			o.Logf("live board unavailable on %s (%d); cards still sync on each write and read. Not retrying.", hostName(o.Client.Host), se.Status)
			return se
		}
		if connected {
			wait = o.Backoff
		}
		o.Logf("board stream: %s; reconnecting (from %s, up to %s)", shortErr(err, o.Client.Host), o.Backoff, o.MaxBackoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, o.MaxBackoff)
	}
}

var errIdle = errors.New("stream idle")

// liveSession is one connection: open, catch up, pull after each burst,
// until the stream ends. connected says whether it opened at all.
func liveSession(ctx context.Context, o LiveOptions, ws string) (connected bool, err error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, err := o.Client.BoardEventsAs(sctx, ws, o.Hello)
	if err != nil {
		return false, err
	}
	defer body.Close()
	liveConnected.Store(true)
	defer liveConnected.Store(false)
	o.Logf("board stream connected (%s)", hostName(o.Client.Host))

	type line struct {
		s   string
		err error
	}
	lines := make(chan line, 64)
	go func() {
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case lines <- line{s: sc.Text()}:
			case <-sctx.Done():
				return
			}
		}
		err := sc.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case lines <- line{err: err}:
		case <-sctx.Done():
		}
	}()

	pullNow := func() {
		pctx, pcancel := context.WithTimeout(ctx, o.PullTimeout)
		defer pcancel()
		res, err := PullCards(pctx, o.Client, o.Project)
		if o.OnPull != nil {
			o.OnPull(res, err)
		}
		if err != nil {
			if ctx.Err() == nil {
				o.Logf("card pull: %s", shortErr(err, o.Client.Host))
			}
			return
		}
		if o.OnChange != nil {
			for _, ch := range res.Changes {
				o.OnChange(ch)
			}
		}
	}

	// Catch up on whatever happened while not connected.
	fire := time.After(0)
	pending := false
	last := time.Now()
	idle := time.NewTicker(min(o.Idle/3, 5*time.Second))
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case l := <-lines:
			if l.err != nil {
				return true, l.err
			}
			last = time.Now()
			switch {
			case l.s == "":
				if pending && fire == nil {
					fire = time.After(o.Debounce)
				}
				pending = false
			case strings.HasPrefix(l.s, ":"):
				// comment: ": subscribed", ": ping"
			case strings.HasPrefix(l.s, "event:"), strings.HasPrefix(l.s, "data:"):
				// Every event (resync included) means: pull the card tables
				// after cards_cursor, which catches up whatever was dropped.
				pending = true
			}
		case <-fire:
			fire = nil
			pullNow()
		case <-idle.C:
			if time.Since(last) > o.Idle {
				return true, errIdle
			}
		}
	}
}

func hostName(h string) string {
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	return strings.TrimRight(h, "/")
}

// shortErr is an error short enough for one line.
func shortErr(err error, host string) string {
	if err == nil {
		return "closed"
	}
	switch {
	case errors.Is(err, ErrBusy):
		return ErrBusy.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return hostName(host) + " did not answer in time"
	case errors.Is(err, io.EOF):
		return "closed by the server"
	case errors.Is(err, errIdle):
		return "no ping from the server"
	case strings.Contains(err.Error(), "unreachable"):
		return hostName(host) + " unreachable"
	}
	var p *api.Problem
	if errors.As(err, &p) {
		return p.Error()
	}
	s := err.Error()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
