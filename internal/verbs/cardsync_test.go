package verbs

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
)

func taskIn(title string) CardNewIn {
	return CardNewIn{Type: "task", Title: title, Sections: map[string]any{"task": map[string]any{"what": title, "soThat": "it syncs"}},
		Acceptance: []string{"it syncs, proven by the test"}}
}

// GRS-51: card new pushes at once and answers with the API's key; the
// other card verbs carry sync.pushed too.
func TestCardWriteThroughOnline(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)

	// Another machine took WS-1.
	other := fakeapi.Workspace(t)
	fakeapi.Ready(t, other)
	if _, err := CardNew(Ctx{Project: other, Agent: "elsewhere"}, taskIn("Theirs")); err != nil {
		t.Fatal(err)
	}

	p := fakeapi.Workspace(t)
	fakeapi.Ready(t, p)
	var notes bytes.Buffer
	c := Ctx{Project: p, Agent: "claude-local", Notes: &notes}
	out, err := CardNew(c, taskIn("Mine"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Key != "WS-2" || out.Sync == nil || !out.Sync.Pushed || out.Sync.Note != "" {
		t.Fatalf("card new: key %s sync %+v", out.Key, out.Sync)
	}
	if row := f.Row("cards", out.ID); row["key"] != "WS-2" {
		t.Fatalf("API row: %v", row)
	}
	cm, err := CardCommentAdd(c, CardCommentIn{Key: out.Key, Body: "write-through test"})
	if err != nil || cm.Sync == nil || !cm.Sync.Pushed {
		t.Fatalf("comment: %+v %v", cm, err)
	}
	mv, err := CardMove(c, CardMoveIn{Key: out.Key, State: "Testing"})
	if err != nil || !mv.Sync.Pushed {
		t.Fatalf("move: %+v %v", mv, err)
	}
	if row := f.Row("cards", out.ID); row["stateId"] != f.StateID("Testing") {
		t.Fatalf("move not on the API: %v", row)
	}
	found := false
	for _, r := range f.RowsOf("card_comments") {
		if r["body"] == "write-through test" {
			found = true
		}
	}
	if !found {
		t.Fatal("comment not on the API")
	}
	if notes.Len() != 0 {
		t.Fatalf("notes online: %q", notes.String())
	}
	// Reads pulled first and are fresh.
	l, err := CardList(c, CardListIn{})
	if err != nil || l.Stale {
		t.Fatalf("list: stale=%v %v", l.Stale, err)
	}
	if len(l.Cards) != 2 {
		t.Fatalf("list after backstop pull: %d cards (want the other machine's too)", len(l.Cards))
	}
}

// Offline: the write succeeds locally, one note on Notes (the CLI's
// stderr) and in the result, and the next push carries it.
func TestCardWriteThroughOffline(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	fakeapi.Login(t, deadURL)

	p := fakeapi.Workspace(t)
	fakeapi.Ready(t, p)
	var notes bytes.Buffer
	c := Ctx{Project: p, Agent: "claude-local", Notes: &notes}
	out, err := CardNew(c, taskIn("Offline"))
	if err != nil {
		t.Fatalf("offline write failed: %v", err)
	}
	if out.Key != "WS-1" || out.Sync == nil || out.Sync.Pushed || !strings.Contains(out.Sync.Note, "not pushed") {
		t.Fatalf("offline: %s %+v", out.Key, out.Sync)
	}
	if got := notes.String(); strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "gears: saved locally; not pushed") {
		t.Fatalf("notes: %q", got)
	}
	// Reads use the cache and say so, quickly.
	start := time.Now()
	l, err := CardList(c, CardListIn{})
	if err != nil || !l.Stale || len(l.Cards) != 1 {
		t.Fatalf("offline list: %+v %v", l, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("offline read took %s", d)
	}

	// Back online: the next write carries the queued one too.
	fakeapi.Login(t, srv.URL)
	if _, err := CardCommentAdd(c, CardCommentIn{Key: out.Key, Body: "back"}); err != nil {
		t.Fatal(err)
	}
	if row := f.Row("cards", out.ID); row["title"] != "Offline" {
		t.Fatalf("queued card not pushed: %v", row)
	}
}
