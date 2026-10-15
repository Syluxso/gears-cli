package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/sync"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// lockedBuf is stderr for a loop running in another goroutine.
type lockedBuf struct {
	mu gosync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := strings.TrimSpace(l.b.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// waitLines waits until the log has n lines.
func (l *lockedBuf) waitLines(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := l.lines(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waited for %d log lines, have %q", n, l.lines())
	return nil
}

// fakeClock is the loop's ticker: the test ticks it by hand.
type fakeClock struct {
	c       chan time.Time
	stopped chan struct{}
}

func newFakeClock() *fakeClock {
	return &fakeClock{c: make(chan time.Time), stopped: make(chan struct{})}
}

func (f *fakeClock) ticker() (<-chan time.Time, func()) {
	return f.c, func() { close(f.stopped) }
}

// tick hands the loop one tick; it blocks until the loop takes it, which
// is after the previous sync has finished.
func (f *fakeClock) tick(t *testing.T) {
	t.Helper()
	select {
	case f.c <- time.Now():
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not take the tick")
	}
}

// start runs the loop until the returned stop is called; stop cancels and
// waits for the loop to end and stop its ticker.
func (f *fakeClock) start(t *testing.T, a autoSync) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.loop(ctx); close(done) }()
	return func() {
		t.Helper()
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the loop did not stop on cancel")
		}
		select {
		case <-f.stopped:
		default:
			t.Fatal("the loop did not stop its ticker")
		}
	}
}

func taskIn(title string) verbs.CardNewIn {
	return verbs.CardNewIn{Type: "task", Title: title, Sections: map[string]any{"task": map[string]any{"what": title, "soThat": "it syncs"}},
		Acceptance: []string{"it syncs, proven by the test"}}
}

func TestSyncInterval(t *testing.T) {
	cases := []struct {
		set  bool
		flag int
		env  string
		want time.Duration
		err  bool
	}{
		{false, 0, "", 0, false},
		{false, 0, "5", 5 * time.Minute, false},
		{false, 0, " 0 ", 0, false},
		{true, 2, "5", 2 * time.Minute, false}, // the flag wins
		{true, 0, "5", 0, false},               // even when it says off
		{false, 0, "soon", 0, true},
		{true, -1, "", 0, true},
	}
	for _, c := range cases {
		got, err := syncInterval(c.set, c.flag, c.env)
		if got != c.want || (err != nil) != c.err {
			t.Errorf("syncInterval(%v, %d, %q) = %v, %v; want %v, err %v", c.set, c.flag, c.env, got, err, c.want, c.err)
		}
	}
}

// GRS-25: one sync at startup, one per tick, a line on stderr each, and
// the loop stops on cancel. Real syncs against the fake API.
func TestPeriodicSyncStartAndTicks(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	fakeapi.Ready(t, p)
	c := verbs.Ctx{Project: p, Agent: "tester"}

	clock, log := newFakeClock(), &lockedBuf{}
	stop := clock.start(t, autoSync{ticker: clock.ticker, log: log,
		run: func(ctx context.Context) (*sync.Report, error) { return verbs.SyncCtx(ctx, c, verbs.SyncIn{}) }})

	got := log.waitLines(t, 1)
	_, pulls0, _ := f.Counts()
	if !strings.HasPrefix(got[0], "gears sync: pushed 0, pulled ") || pulls0 == 0 {
		t.Fatalf("startup sync: %q, %d pulls", got, pulls0)
	}
	// A card written elsewhere arrives with the next tick.
	other := fakeapi.Workspace(t)
	fakeapi.Ready(t, other)
	if _, err := verbs.CardNew(verbs.Ctx{Project: other, Agent: "elsewhere"}, taskIn("Theirs")); err != nil {
		t.Fatal(err)
	}
	clock.tick(t)
	got = log.waitLines(t, 2)
	if !strings.HasPrefix(got[1], "gears sync: pushed 0, pulled ") || got[1] == "gears sync: pushed 0, pulled 0" {
		t.Fatalf("tick sync: %q", got)
	}
	l, err := verbs.CardList(c, verbs.CardListIn{})
	if err != nil || len(l.Cards) != 1 {
		t.Fatalf("after the tick: %v cards, %v", l, err)
	}
	clock.tick(t)
	log.waitLines(t, 3)
	_, pulls, _ := f.Counts()
	if pulls <= pulls0 {
		t.Fatalf("pulls %d -> %d", pulls0, pulls)
	}
	stop()
	if n := len(log.lines()); n != 3 {
		t.Fatalf("lines after stop: %q", log.lines())
	}
}

// GRS-25: a card push that starts while the periodic sync is in flight
// waits for it (one shared lock), and one that will not wait long enough
// gives up with sync.ErrBusy instead of overlapping.
func TestPeriodicSyncNoOverlapWithCardPush(t *testing.T) {
	f := fakeapi.New("WS")
	f.PushDelay = 400 * time.Millisecond
	inPush := make(chan struct{})
	var once gosync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			once.Do(func() { close(inPush) })
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	fakeapi.Ready(t, p)
	// A card saved while card sync was off: queued for the next push.
	t.Setenv("GEARS_CARD_SYNC", "off")
	c := verbs.Ctx{Project: p, Agent: "tester"}
	if _, err := verbs.CardNew(c, taskIn("Queued")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEARS_CARD_SYNC", "")

	var mu gosync.Mutex
	var syncEnd time.Time
	clock, log := newFakeClock(), &lockedBuf{}
	stop := clock.start(t, autoSync{ticker: clock.ticker, log: log,
		run: func(ctx context.Context) (*sync.Report, error) {
			r, err := verbs.SyncCtx(ctx, c, verbs.SyncIn{})
			mu.Lock()
			syncEnd = time.Now()
			mu.Unlock()
			return r, err
		}})
	defer stop()

	select {
	case <-inPush: // the startup sync is mid-request, holding the lock
	case <-time.After(10 * time.Second):
		t.Fatal("the startup sync never pushed")
	}
	cl, err := api.Open(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = sync.PushCards(short, cl, p)
	cancel()
	if !errors.Is(err, sync.ErrBusy) {
		t.Fatalf("impatient card push during the sync: %v (want ErrBusy)", err)
	}
	long, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := sync.PushCards(long, cl, p)
	pushEnd := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	got := log.waitLines(t, 1)
	mu.Lock()
	end := syncEnd
	mu.Unlock()
	if end.IsZero() || pushEnd.Before(end) {
		t.Fatalf("card push ended %v, sync ended %v: they overlapped", pushEnd, end)
	}
	if res == nil || !strings.HasPrefix(got[0], "gears sync: pushed ") || strings.HasPrefix(got[0], "gears sync: pushed 0,") {
		t.Fatalf("card push after the sync: %+v, log %q", res, got)
	}
	if row := f.RowsOf("cards"); len(row) != 1 || row[0]["title"] != "Queued" {
		t.Fatalf("the queued card on the API: %v", row)
	}
}

// GRS-25: not logged in, the loop says so once and then skips quietly.
func TestPeriodicSyncQuietWhenNotLoggedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	p := fakeapi.Workspace(t)
	c := verbs.Ctx{Project: p, Agent: "tester"}
	clock, log := newFakeClock(), &lockedBuf{}
	stop := clock.start(t, autoSync{ticker: clock.ticker, log: log,
		run: func(ctx context.Context) (*sync.Report, error) { return verbs.SyncCtx(ctx, c, verbs.SyncIn{}) }})
	for i := 0; i < 3; i++ {
		clock.tick(t)
	}
	clock.tick(t) // taken only after the third tick's sync finished
	stop()
	got := log.lines()
	if len(got) != 1 || !strings.Contains(got[0], "skipped, not logged in") {
		t.Fatalf("log: %q", got)
	}
}

// GRS-25: a skip is logged again only when the reason changes; a sync that
// goes through always gets its line.
func TestPeriodicSyncLogsOnStateChange(t *testing.T) {
	offline := fmt.Errorf("pull: %w", errors.New("http://127.0.0.1:1 unreachable: connection refused"))
	results := []error{api.ErrNotLoggedIn, api.ErrNotLoggedIn, offline, offline, nil, api.ErrNotLoggedIn, errors.New("boom"), errors.New("boom")}
	i := 0
	clock, log := newFakeClock(), &lockedBuf{}
	stop := clock.start(t, autoSync{ticker: clock.ticker, log: log,
		run: func(ctx context.Context) (*sync.Report, error) {
			if i == len(results) {
				<-ctx.Done() // the extra tick's sync runs until stop
				return nil, ctx.Err()
			}
			err := results[i]
			i++
			if err != nil {
				return nil, err
			}
			return &sync.Report{Pushed: 2, Pulled: 3}, nil
		}})
	for range len(results) { // the startup sync plus len-1 ticks; the last tick is taken after the last sync
		clock.tick(t)
	}
	stop()
	want := []string{
		"gears sync: skipped, not logged in (gears login); skipping quietly until that changes",
		"gears sync: skipped, offline (" + api.Host() + " unreachable); skipping quietly until that changes",
		"gears sync: pushed 2, pulled 3",
		"gears sync: skipped, not logged in (gears login); skipping quietly until that changes",
		"gears sync: boom",
		"gears sync: boom",
	}
	if got := log.lines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("log:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// runLog is the loop's GRS-38 recorder writing real workspace entries.
func runLog(c verbs.Ctx, calls *int) func(string) error {
	return func(text string) error {
		*calls++
		_, err := session.AppendFor(c.Project, "", "done", c.Agent, text)
		return err
	}
}

// GRS-38: the first sync that goes through writes one workspace entry and
// pushes it; ticks write nothing; shutdown writes and pushes one stop entry.
func TestRunRecordsStartAndStop(t *testing.T) {
	f := fakeapi.New("WS")
	// The fake keeps card tables only; this records the session entries
	// each push carries, by id.
	var mu gosync.Mutex
	entries := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			b, _ := io.ReadAll(r.Body)
			var in struct {
				Rows map[string][]map[string]any `json:"rows"`
			}
			_ = json.Unmarshal(b, &in)
			mu.Lock()
			for _, m := range in.Rows["session_entries"] {
				entries[fmt.Sprint(m["id"])] = m
			}
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	pushed := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var out []map[string]any
		for _, m := range entries {
			out = append(out, m)
		}
		return out
	}
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	fakeapi.Ready(t, p)
	c := verbs.Ctx{Project: p, Agent: "tester"}
	started, stopped := runEntries(5, "box", "tester")
	if started != "gears run started: auto-sync every 5 min on box as tester" {
		t.Fatalf("start text %q", started)
	}

	calls := 0
	clock, log := newFakeClock(), &lockedBuf{}
	stop := clock.start(t, autoSync{ticker: clock.ticker, log: log, record: runLog(c, &calls), started: started, stopped: stopped,
		run: func(ctx context.Context) (*sync.Report, error) { return verbs.SyncCtx(ctx, c, verbs.SyncIn{}) }})
	clock.tick(t) // taken after the startup sync, the record and its push
	clock.tick(t)
	clock.tick(t) // taken after the second tick's sync
	rows := pushed()
	if len(rows) != 1 || rows[0]["text"] != started || rows[0]["kind"] != "done" || rows[0]["agent"] != "tester" {
		t.Fatalf("after start and ticks, the API has %v", rows)
	}
	stop()
	if calls != 2 {
		t.Fatalf("record called %d times, want 2", calls)
	}
	rows = pushed()
	if len(rows) != 2 {
		t.Fatalf("after stop, the API has %v", rows)
	}
	texts := []string{rows[0]["text"].(string), rows[1]["text"].(string)}
	if !(texts[0] == stopped || texts[1] == stopped) {
		t.Fatalf("no stop entry on the API: %q", texts)
	}
	for _, l := range log.lines() {
		if strings.HasPrefix(l, "gears run:") {
			t.Fatalf("unexpected run line: %q", log.lines())
		}
	}
}

// GRS-38: not logged in, nothing is written, not even at stop.
func TestRunRecordsNothingWhenNotLoggedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	p := fakeapi.Workspace(t)
	c := verbs.Ctx{Project: p, Agent: "tester"}
	calls := 0
	clock, log := newFakeClock(), &lockedBuf{}
	started, stopped := runEntries(5, "box", "tester")
	stop := clock.start(t, autoSync{ticker: clock.ticker, log: log, record: runLog(c, &calls), started: started, stopped: stopped,
		run: func(ctx context.Context) (*sync.Report, error) { return verbs.SyncCtx(ctx, c, verbs.SyncIn{}) }})
	clock.tick(t)
	clock.tick(t)
	stop()
	if calls != 0 {
		t.Fatalf("record called %d times offline", calls)
	}
	if es, err := session.Recent(p, 1); err != nil || len(es) != 0 {
		t.Fatalf("entries written offline: %v, %v", es, err)
	}
}

// GRS-38: offline at start, the entry waits for the first sync that goes
// through, and is written once.
func TestRunRecordsWhenSyncFirstGoesThrough(t *testing.T) {
	results := []error{api.ErrNotLoggedIn, errors.New("boom"), nil, nil, nil, nil}
	var mu gosync.Mutex
	i := 0
	var got []string
	clock, log := newFakeClock(), &lockedBuf{}
	stop := clock.start(t, autoSync{ticker: clock.ticker, log: log, started: "up", stopped: "down",
		record: func(text string) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, fmt.Sprintf("%s@%d", text, i))
			return nil
		},
		run: func(ctx context.Context) (*sync.Report, error) {
			mu.Lock()
			defer mu.Unlock()
			if i == len(results) {
				return &sync.Report{}, nil
			}
			err := results[i]
			i++
			if err != nil {
				return nil, err
			}
			return &sync.Report{}, nil
		}})
	clock.tick(t) // after the startup skip
	clock.tick(t) // after boom
	clock.tick(t) // after the first sync through, the record and its push
	stop()
	mu.Lock()
	defer mu.Unlock()
	// up after the third sync; down after the fifth (tick sync), before the stop push.
	if strings.Join(got, ",") != "up@3,down@5" {
		t.Fatalf("records %q", got)
	}
	if i != 6 {
		t.Fatalf("%d syncs; want 6 (skip, boom, through, push, tick, stop push)", i)
	}
}
