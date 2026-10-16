// Package fakeapi is a stand-in for gears-api's board, for tests only
// (GRS-51): the sync push and pull of the card tables (with the tables
// filter and its own cursor), the API's card numbers and state merge by
// name, GET board, and the live board stream (Server-Sent Events). Nothing
// outside _test files imports it.
package fakeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/card"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

var cardTables = []string{"card_states", "cards", "card_comments", "card_links", "card_checklists", "card_checklist_items", "card_events"}

func isCard(tbl string) bool {
	for _, t := range cardTables {
		if t == tbl {
			return true
		}
	}
	return false
}

type entry struct {
	seq     int64
	tbl, id string
	op, by  string
	at      time.Time
}

// Server is the fake. The zero value is not usable; call New.
type Server struct {
	mu      sync.Mutex
	Prefix  string
	states  []api.BoardState
	rows    map[string]map[string]json.RawMessage
	log     []entry
	numbers map[string]int
	used    map[int]bool
	subs    map[chan string]struct{}

	// RunbooksOff answers as a gears-api without BYZ_ORCHESTRATOR_URL:
	// the official catalog, abilities, start, complete and skip are 501;
	// record reads (runs, adopted copies) still answer.
	RunbooksOff bool
	// IgnoreTables answers a filtered pull as an API from before GRS-51:
	// everything, no tables echo.
	IgnoreTables bool
	// StreamStatus, when set, answers the board stream with it (404: an API
	// from before GRS-48).
	StreamStatus int
	// PushDelay and PullDelay slow the answers down (timeouts).
	PushDelay, PullDelay time.Duration

	Pushes  int
	Pulls   []string // raw query of each pull
	Streams int      // stream connections opened

	// Presence (GRS-37): each open stream is a connection, as the device
	// "tester" with the stream's X-Gears-Label and X-Gears-Sync-Interval.
	// Others are connections that are not streams here (a browser), listed
	// first. PresenceStatus, when set, answers GET presence with it (404:
	// an API from before GRS-37).
	Others         []api.PresenceConn
	PresenceStatus int
	open           map[chan string]api.PresenceConn
	Hellos         []http.Header // the headers of every stream request

	// Membership (GRS-42): the workspace's members and invites, and the
	// caller (Me, an owner unless MeRole says otherwise). Accounts are the
	// emails that exist (email -> user id); an invite to any other email is
	// the API's no_account problem. MembersStatus, when set, answers every
	// membership route with it and no problem body (an API from before
	// GRS-34).
	Me            string
	MeRole        string
	Accounts      map[string]string
	Members       []api.Member
	Invites       []api.Invite
	MembersStatus int
	inviteSeq     int

	runBodies []map[string]any // GRS-56: every run write's body
}

func New(prefix string) *Server {
	s := &Server{Prefix: prefix, rows: map[string]map[string]json.RawMessage{}, numbers: map[string]int{}, used: map[int]bool{},
		subs: map[chan string]struct{}{}, open: map[chan string]api.PresenceConn{}}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, n := range card.DefaultStates {
		st := api.BoardState{ID: project.NewID(), Name: n, Position: i, IsDefault: i == 0, IsTerminal: i == len(card.DefaultStates)-1, CreatedAt: at, UpdatedAt: at}
		s.states = append(s.states, st)
		b, _ := json.Marshal(st)
		s.put("card_states", st.ID, b, "upsert", "seed")
	}
	return s
}

// put records a row and its change; call with mu held.
func (s *Server) put(tbl, id string, row json.RawMessage, op, by string) {
	if s.rows[tbl] == nil {
		s.rows[tbl] = map[string]json.RawMessage{}
	}
	if row != nil {
		s.rows[tbl][id] = row
	}
	s.log = append(s.log, entry{seq: int64(len(s.log) + 1), tbl: tbl, id: id, op: op, by: by, at: time.Now().UTC()})
}

// broadcast sends one board event to every stream; call with mu held.
func (s *Server) broadcast(typ, cardID string) {
	b, _ := json.Marshal(map[string]string{"type": typ, "cardId": cardID})
	msg := "event: " + typ + "\ndata: " + string(b) + "\n\n"
	for ch := range s.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

// Seq is the last change's seq.
func (s *Server) Seq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.log))
}

// Row is the API's copy of a row, decoded.
func (s *Server) Row(tbl, id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]any
	_ = json.Unmarshal(s.rows[tbl][id], &m)
	return m
}

// RowsOf lists the API's rows of one table.
func (s *Server) RowsOf(tbl string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, raw := range s.rows[tbl] {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		out = append(out, m)
	}
	return out
}

// StateID is the API's id for a state name.
func (s *Server) StateID(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.states {
		if strings.EqualFold(st.Name, name) {
			return st.ID
		}
	}
	return ""
}

// Edit changes a row as another client would (the app's drag and drop):
// fields merged into the API's copy, updatedAt bumped, a change logged and
// one event sent to every stream.
func (s *Server) Edit(tbl, id, by string, fields map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]any
	_ = json.Unmarshal(s.rows[tbl][id], &m)
	if m == nil {
		m = map[string]any{"id": id}
	}
	for k, v := range fields {
		m[k] = v
	}
	m["updatedAt"] = time.Now().UTC().Add(time.Second).Truncate(time.Second).Format(time.RFC3339)
	b, _ := json.Marshal(m)
	s.put(tbl, id, b, "upsert", by)
	s.broadcast("card.updated", id)
}

// Ping sends an SSE comment to every stream.
func (s *Server) Ping() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ": ping\n\n":
		default:
		}
	}
}

// Subscribers is how many streams are open.
func (s *Server) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/board/events"):
		s.stream(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/presence"):
		s.presence(w)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/board"):
		s.mu.Lock()
		b := api.Board{Prefix: s.Prefix, States: s.states}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(b)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/sync"):
		time.Sleep(s.PushDelay)
		s.push(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/sync"):
		time.Sleep(s.PullDelay)
		s.pull(w, r)
	case s.membership(w, r):
	case s.runs(w, r):
	case s.runbooks(w, r):
	default:
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"status":404,"code":"gears.not_found","detail":"not found"}`)
	}
}

// presence answers GET presence: Others, then each open stream, oldest first.
func (s *Server) presence(w http.ResponseWriter) {
	s.mu.Lock()
	status := s.PresenceStatus
	conns := append([]api.PresenceConn{}, s.Others...)
	var streams []api.PresenceConn
	for _, p := range s.open {
		streams = append(streams, p)
	}
	s.mu.Unlock()
	if status != 0 {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"status":%d,"code":"gears.not_found","detail":"not found"}`, status)
		return
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].ConnectedAt.Before(streams[j].ConnectedAt) })
	conns = append(conns, streams...)
	_ = json.NewEncoder(w).Encode(api.Presence{Count: len(conns), Connections: conns})
}

// Hello is the headers of the last stream request.
func (s *Server) Hello() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Hellos) == 0 {
		return http.Header{}
	}
	return s.Hellos[len(s.Hellos)-1]
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	status := s.StreamStatus
	s.Streams++
	s.mu.Unlock()
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	ch := make(chan string, 64)
	label := r.Header.Get("X-Gears-Label")
	if label == "" {
		label = "tester"
	}
	every, _ := strconv.Atoi(r.Header.Get("X-Gears-Sync-Interval"))
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.Hellos = append(s.Hellos, r.Header.Clone())
	s.open[ch] = api.PresenceConn{UserID: "tester@example.test", Email: "tester@example.test", Label: label, DeviceID: "dev-tester",
		DeviceLabel: "tester", AuthMethod: "device", SyncIntervalMinutes: every, ConnectedAt: time.Now().UTC()}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		delete(s.open, ch)
		s.mu.Unlock()
	}()
	_, _ = io.WriteString(w, ": subscribed\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if _, err := io.WriteString(w, msg); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cursor, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Pulls = append(s.Pulls, r.URL.RawQuery)
	filter := q.Get("tables") != "" && !s.IgnoreTables
	out := api.PullOut{Cursor: cursor, Changes: []api.Change{}}
	if filter {
		out.Tables = cardTables
	}
	last := map[string]int64{}
	for _, e := range s.log {
		last[e.tbl+"/"+e.id] = e.seq
	}
	for _, e := range s.log {
		if e.seq <= cursor || (filter && !isCard(e.tbl)) {
			continue
		}
		out.Cursor = e.seq
		if last[e.tbl+"/"+e.id] != e.seq {
			continue
		}
		ch := api.Change{Seq: e.seq, Table: e.tbl, ID: e.id, Op: e.op, By: e.by, At: e.at}
		if e.op == "delete" {
			ch.Deleted = true
		} else {
			ch.Row = s.rows[e.tbl][e.id]
		}
		out.Changes = append(out.Changes, ch)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rows    map[string][]map[string]any `json:"rows"`
		Deletes map[string][]string         `json:"deletes"`
	}
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	by := r.Header.Get("X-Gears-Label")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Pushes++
	out := api.PushOut{Results: []api.PushResult{}}
	remap := map[string]string{}
	// Last write wins, ties to the push (the API's "updated_at <=
	// EXCLUDED.updated_at"); activity is append-only.
	newer := func(tbl, id string, m map[string]any) bool {
		old, ok := s.rows[tbl][id]
		if !ok {
			return true
		}
		if tbl == "card_events" {
			return false
		}
		var o map[string]any
		_ = json.Unmarshal(old, &o)
		ou, _ := o["updatedAt"].(string)
		nu, _ := m["updatedAt"].(string)
		return nu >= ou
	}
	for _, tbl := range cardTables {
		for _, m := range in.Rows[tbl] {
			id, _ := m["id"].(string)
			res := api.PushResult{Table: tbl, ID: id}
			switch tbl {
			case "card_states":
				name, _ := m["name"].(string)
				for _, st := range s.states {
					if st.ID != id && strings.EqualFold(st.Name, name) {
						remap[id] = st.ID
						res.Assigned = &api.Assigned{ID: st.ID}
					}
				}
				if res.Assigned == nil && newer(tbl, id, m) {
					rb, _ := json.Marshal(m)
					s.put(tbl, id, rb, "upsert", by)
					res.Applied = true
				}
			case "cards":
				if to, ok := remap[fmt.Sprint(m["stateId"])]; ok {
					m["stateId"] = to
				}
				n, ok := s.numbers[id]
				if !ok {
					want := 0
					if f, ok := m["number"].(float64); ok {
						want = int(f)
					}
					n = want
					if want <= 0 || s.used[want] {
						n = 0
						for u := range s.used {
							n = max(n, u)
						}
						n++
					}
					s.numbers[id], s.used[n] = n, true
				}
				key := s.Prefix + "-" + strconv.Itoa(n)
				m["number"], m["key"] = n, key
				res.Assigned = &api.Assigned{Number: n, Key: key}
				if newer(tbl, id, m) {
					rb, _ := json.Marshal(m)
					s.put(tbl, id, rb, "upsert", by)
					res.Applied = true
					s.broadcast("card.updated", id)
				}
			default:
				if newer(tbl, id, m) {
					rb, _ := json.Marshal(m)
					s.put(tbl, id, rb, "upsert", by)
					res.Applied = true
					cid, _ := m["cardId"].(string)
					s.broadcast("card.activity", cid)
				}
			}
			out.Results = append(out.Results, res)
			if res.Applied {
				out.Applied++
			} else {
				out.Skipped++
			}
		}
	}
	for tbl, ids := range in.Deletes {
		for _, id := range ids {
			_, had := s.rows[tbl][id]
			if had {
				delete(s.rows[tbl], id)
				s.put(tbl, id, nil, "delete", by)
				s.broadcast("card.deleted", id)
			}
			out.Results = append(out.Results, api.PushResult{Table: tbl, ID: id, Applied: had})
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

// SetStreamStatus changes how the board stream answers (0: a stream).
func (s *Server) SetStreamStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StreamStatus = code
}

// SetPullDelay slows every pull down.
func (s *Server) SetPullDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PullDelay = d
}

// Counts is pushes, pulls and stream connections so far.
func (s *Server) Counts() (pushes, pulls, streams int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Pushes, len(s.Pulls), s.Streams
}

// LastPull is the raw query of the latest pull.
func (s *Server) LastPull() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Pulls) == 0 {
		return ""
	}
	return s.Pulls[len(s.Pulls)-1]
}

// Login points the CLI at host with a saved token in a throwaway home
// (HOME and USERPROFILE, which Go reads on Windows), for one test.
func Login(t *testing.T, host string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", host)
	t.Setenv("GEARS_CARD_SYNC", "")
	creds := &api.Credentials{Default: host, Hosts: map[string]api.Credential{host: {Token: "test-token", Label: "tester"}}}
	if err := creds.Save(); err != nil {
		t.Fatal(err)
	}
}

// Ready marks a cache as having synced its board once (what the first
// gears sync with cards leaves behind), so card-only push and pull run.
func Ready(t *testing.T, p *project.Project) {
	t.Helper()
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO sync_state (key, value) VALUES ('cards_scope', 'workspace') ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		t.Fatal(err)
	}
}

// Workspace is a fresh workspace root bound to itself, prefix WS.
func Workspace(t *testing.T) *project.Project {
	t.Helper()
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	return p
}
