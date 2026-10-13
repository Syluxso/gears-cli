package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/card"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

// GRS-51: cards sync immediately. Write-through push, card-only pull with
// its own cursor, the board stream, the read backstop.

func liveCache(t *testing.T) *project.Project {
	t.Helper()
	p := fakeapi.Workspace(t)
	fakeapi.Ready(t, p)
	return p
}

func syncState(t *testing.T, p *project.Project, key string) string {
	t.Helper()
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, _ := stateDB(db, key)
	return v
}

// A write pushes at once and takes the API's number: another machine holds
// 1, so the card proposed as WS-1 is WS-2 here before the verb returns.
func TestPushCardsAssignsKeyAtOnce(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	other := liveCache(t)
	theirs := newTask(t, other, "Theirs")
	if _, err := PushCards(context.Background(), api.Anonymous(srv.URL), other); err != nil {
		t.Fatal(err)
	}
	if c, _ := card.Get(other, theirs.ID); c.Key != "WS-1" {
		t.Fatalf("first card: %s", c.Key)
	}

	p := liveCache(t)
	mine := newTask(t, p, "Mine")
	if mine.Key != "WS-1" {
		t.Fatalf("offline proposal: %s", mine.Key)
	}
	if _, err := card.AddComment(p, mine.Key, "evidence", "tester"); err != nil {
		t.Fatal(err)
	}
	res, err := PushCards(context.Background(), api.Anonymous(srv.URL), p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 || res.Renumbered != 1 {
		t.Fatalf("push: %+v", res)
	}
	got, err := card.Get(p, mine.ID)
	if err != nil || got.Key != "WS-2" {
		t.Fatalf("after push: %+v %v", got, err)
	}
	if row := f.Row("cards", mine.ID); row["key"] != "WS-2" || row["stateId"] != f.StateID("Backlog") {
		t.Fatalf("API row: %v", row)
	}
	if n := len(f.RowsOf("card_comments")); n != 1 {
		t.Fatalf("API comments: %d", n)
	}
	// The local seeded states took the API's ids.
	sts, _ := card.States(p)
	for _, s := range sts {
		if s.ID != f.StateID(s.Name) {
			t.Fatalf("state %s: local %s api %s", s.Name, s.ID, f.StateID(s.Name))
		}
	}
	if syncState(t, p, "cards_pushed_at") == "" {
		t.Fatal("watermark not set")
	}
}

// A cache that never synced its board does not push piecemeal.
func TestPushCardsNeedsFirstSync(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := fakeapi.Workspace(t)
	newTask(t, p, "Early")
	if _, err := PushCards(context.Background(), api.Anonymous(srv.URL), p); !errors.Is(err, ErrCardsNotReady) {
		t.Fatalf("want ErrCardsNotReady, got %v", err)
	}
	if pushes, _, _ := f.Counts(); pushes != 0 {
		t.Fatalf("pushed %d times", pushes)
	}
}

// Offline (logged in, API down): the write stands, CardsUp says so in one
// line, the rows stay queued, and the next push (here, once the API is
// back) carries them.
func TestCardsUpOfflineQueues(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	fakeapi.Login(t, deadURL)
	p := liveCache(t)
	c := newTask(t, p, "Offline")
	start := time.Now()
	pushed, note := CardsUp(p, 3*time.Second)
	if pushed || !strings.Contains(note, "saved locally; not pushed") || !strings.Contains(note, "gears sync will push it") || strings.Contains(note, "\n") {
		t.Fatalf("offline: pushed=%v note=%q", pushed, note)
	}
	if time.Since(start) > 3500*time.Millisecond {
		t.Fatalf("write-through took %s", time.Since(start))
	}
	if syncState(t, p, "cards_pushed_at") != "" {
		t.Fatal("watermark moved while offline")
	}

	// Not logged in at all: the same, with its own reason.
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	if pushed, note := CardsUp(p, time.Second); pushed || !strings.Contains(note, "not logged in") {
		t.Fatalf("logged out: %v %q", pushed, note)
	}

	// Back online: the queued card goes up with the next push.
	fakeapi.Login(t, srv.URL)
	if pushed, note := CardsUp(p, 3*time.Second); !pushed || note != "" {
		t.Fatalf("online: %v %q", pushed, note)
	}
	if row := f.Row("cards", c.ID); row["title"] != "Offline" {
		t.Fatalf("queued card not pushed: %v", row)
	}

	// Off switch: nothing at all.
	t.Setenv("GEARS_CARD_SYNC", "off")
	if pushed, note := CardsUp(p, time.Second); pushed || note != "" {
		t.Fatalf("off: %v %q", pushed, note)
	}
}

// A card-only pull asks for tables=cards from its own cursor, applies, and
// leaves the main cursor alone; an API that ignores the filter gets a full
// pull on the main cursor instead.
func TestPullCardsOwnCursor(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	a, b := liveCache(t), liveCache(t)
	cl := api.Anonymous(srv.URL)
	x := newTask(t, a, "From A")
	if _, err := PushCards(context.Background(), cl, a); err != nil {
		t.Fatal(err)
	}
	db, _ := store.Open(b)
	_ = setState(db, "cursor", "0")
	db.Close()

	res, err := PullCards(context.Background(), cl, b)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fallback || res.Applied == 0 || !strings.Contains(f.LastPull(), "tables=cards") || !strings.Contains(f.LastPull(), "cursor=0") {
		t.Fatalf("pull: %+v query %s", res, f.LastPull())
	}
	got, err := card.Get(b, x.ID)
	if err != nil || got.Key != "WS-1" || got.Title != "From A" {
		t.Fatalf("B's copy: %+v %v", got, err)
	}
	if cc := syncState(t, b, "cards_cursor"); cc != fmt.Sprint(f.Seq()) {
		t.Fatalf("cards_cursor %s, want %d", cc, f.Seq())
	}
	if c := syncState(t, b, "cursor"); c != "0" {
		t.Fatalf("main cursor moved to %s", c)
	}
	// Created shows up as a change line.
	found := false
	for _, ch := range res.Changes {
		if ch.Key == "WS-1" && strings.HasPrefix(ch.What, "created") {
			found = true
		}
	}
	if !found {
		t.Fatalf("changes: %+v", res.Changes)
	}
	// Pulling again from the stored cursor is empty.
	res, err = PullCards(context.Background(), cl, b)
	if err != nil || res.Pulled != 0 || !strings.Contains(f.LastPull(), fmt.Sprintf("cursor=%d", f.Seq())) {
		t.Fatalf("second pull: %+v %v %s", res, err, f.LastPull())
	}

	// An older API: the filter is ignored; the main pull runs instead.
	f.IgnoreTables = true
	f.Edit("cards", x.ID, "app", map[string]any{"title": "Renamed"})
	res, err = PullCards(context.Background(), cl, b)
	if err != nil || !res.Fallback {
		t.Fatalf("fallback: %+v %v", res, err)
	}
	if got, _ := card.Get(b, x.ID); got.Title != "Renamed" {
		t.Fatalf("fallback did not apply: %s", got.Title)
	}
	if c := syncState(t, b, "cursor"); c != fmt.Sprint(f.Seq()) {
		t.Fatalf("fallback main cursor %s, want %d", c, f.Seq())
	}
}

// liveRun runs Live in the background and collects its log and changes.
type liveRun struct {
	mu      stdsync.Mutex
	logs    []string
	changes []CardChange
	done    chan error
	cancel  context.CancelFunc
}

func startLive(t *testing.T, url string, p *project.Project, tune func(*LiveOptions)) *liveRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lr := &liveRun{done: make(chan error, 1), cancel: cancel}
	o := LiveOptions{Client: api.Anonymous(url), Project: p,
		Logf: func(f string, a ...any) {
			lr.mu.Lock()
			lr.logs = append(lr.logs, fmt.Sprintf(f, a...))
			lr.mu.Unlock()
		},
		OnChange: func(c CardChange) {
			lr.mu.Lock()
			lr.changes = append(lr.changes, c)
			lr.mu.Unlock()
		},
		Backoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond}
	if tune != nil {
		tune(&o)
	}
	go func() { lr.done <- Live(ctx, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-lr.done:
		case <-time.After(3 * time.Second):
			t.Error("Live did not stop")
		}
	})
	return lr
}

func (lr *liveRun) text() string {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	var lines []string
	for _, c := range lr.changes {
		lines = append(lines, c.String())
	}
	return strings.Join(lr.logs, "\n") + "\n--\n" + strings.Join(lines, "\n")
}

func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("not within %s: %s", within, what)
}

// The stream: an edit made elsewhere (the app moves a card) reaches this
// cache within ~2s of the event, and the change reads as one line.
func TestLiveStreamPullsOnEvent(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close) // after Live stops (cleanups run last first)
	a, b := liveCache(t), liveCache(t)
	cl := api.Anonymous(srv.URL)
	x := newTask(t, a, "Live card")
	if _, err := PushCards(context.Background(), cl, a); err != nil {
		t.Fatal(err)
	}

	lr := startLive(t, srv.URL, b, nil)
	waitFor(t, 2*time.Second, "subscribed", func() bool { return f.Subscribers() == 1 })
	// The catch-up pull on connect brings the card.
	waitFor(t, 2*time.Second, "catch-up pull", func() bool { _, err := card.Get(b, x.ID); return err == nil })
	if !LiveConnected() {
		t.Fatal("LiveConnected false while the stream is open")
	}

	sent := time.Now()
	f.Edit("cards", x.ID, "browser", map[string]any{"stateId": f.StateID("Testing"), "state": "Testing"})
	waitFor(t, 2*time.Second, "move applied", func() bool { c, _ := card.Get(b, x.ID); return c != nil && c.State == "Testing" })
	t.Logf("event -> applied in %s", time.Since(sent).Round(time.Millisecond))
	waitFor(t, time.Second, "change line", func() bool { return strings.Contains(lr.text(), "WS-1 moved → Testing (by browser)") })

	// A burst of events is one pull (debounce).
	_, before, _ := f.Counts()
	for i := 0; i < 5; i++ {
		f.Edit("cards", x.ID, "browser", map[string]any{"title": fmt.Sprintf("burst %d", i)})
	}
	waitFor(t, 2*time.Second, "burst applied", func() bool { c, _ := card.Get(b, x.ID); return c.Title == "burst 4" })
	time.Sleep(400 * time.Millisecond)
	if _, after, _ := f.Counts(); after-before > 2 {
		t.Fatalf("burst of 5 events made %d pulls", after-before)
	}
	// Pings are not events.
	_, before, _ = f.Counts()
	f.Ping()
	time.Sleep(450 * time.Millisecond)
	if _, after, _ := f.Counts(); after != before {
		t.Fatalf("a ping pulled")
	}
	if !strings.Contains(lr.text(), "board stream connected") {
		t.Fatalf("log: %s", lr.text())
	}
}

// An API without the live board (404) or a refused token (401): Live says
// so once and stops, no retries.
func TestLiveStopsOnPermanentAnswer(t *testing.T) {
	for _, code := range []int{404, 401, 501} {
		f := fakeapi.New("WS")
		f.SetStreamStatus(code)
		srv := httptest.NewServer(f)
		p := liveCache(t)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var logs []string
		err := Live(ctx, LiveOptions{Client: api.Anonymous(srv.URL), Project: p, Backoff: 10 * time.Millisecond,
			Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
		cancel()
		srv.Close()
		var se *api.StreamError
		if !errors.As(err, &se) || se.Status != code {
			t.Fatalf("%d: err %v", code, err)
		}
		if _, _, streams := f.Counts(); streams != 1 {
			t.Fatalf("%d: %d attempts", code, streams)
		}
		if len(logs) != 1 || !strings.Contains(logs[0], fmt.Sprintf("unavailable on 127.0.0.1")) || !strings.Contains(logs[0], fmt.Sprint(code)) {
			t.Fatalf("%d: logs %q", code, logs)
		}
	}
}

// A transient failure (503, the API restarting) reconnects with backoff
// and, once back, catches up.
func TestLiveReconnects(t *testing.T) {
	f := fakeapi.New("WS")
	f.SetStreamStatus(503)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := liveCache(t)
	lr := startLive(t, srv.URL, p, nil)
	waitFor(t, 2*time.Second, "retries", func() bool { _, _, n := f.Counts(); return n >= 3 })
	f.SetStreamStatus(0)
	waitFor(t, 2*time.Second, "reconnected", func() bool { return f.Subscribers() == 1 })
	if s := lr.text(); strings.Count(s, "reconnecting") != 1 || !strings.Contains(s, "board stream connected") {
		t.Fatalf("logs (a repeated failure is said once): %s", s)
	}
	// The server drops the stream; Live comes back.
	srv.CloseClientConnections()
	waitFor(t, 2*time.Second, "back after a drop", func() bool { _, _, n := f.Counts(); return n >= 5 && f.Subscribers() == 1 })
}

// The read backstop: an API slower than the timeout leaves the read to the
// cache, quickly, and says it is stale; a quick one is fresh.
func TestCardsBackstop(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	p := liveCache(t)
	if stale := CardsBackstop(p, time.Second); stale {
		t.Fatal("quick API reported stale")
	}
	f.SetPullDelay(time.Second)
	start := time.Now()
	if stale := CardsBackstop(p, 200*time.Millisecond); !stale {
		t.Fatal("slow API not stale")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("backstop waited %s", d)
	}
	// Not synced yet, or not logged in: nothing to be stale against.
	if CardsBackstop(fakeapi.Workspace(t), 200*time.Millisecond) {
		t.Fatal("unsynced cache stale")
	}
}
