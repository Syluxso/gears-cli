package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
)

// GRS-80: gears-api answers 410 gears.workspace_deleted. The cache is
// marked deleted, keeps its data, and refuses reads; a later successful
// sync (after a restore) clears the mark.

const deletedMsg = "was deleted on gears-api (restore: gears workspace restore)"

func TestSyncWorkspaceDeleted(t *testing.T) {
	res, err := project.Init(t.TempDir(), "synctest", "ST")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	p.Workspace = p.ID
	if _, err := session.AppendFor(p, "", "done", "tester", "before the delete"); err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cl := api.Anonymous(srv.URL)
	if _, err := Run(context.Background(), cl, p); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	f.gone = true
	f.mu.Unlock()
	_, err = Run(context.Background(), cl, p)
	if !errors.Is(err, store.ErrWorkspaceDeleted) || !strings.Contains(err.Error(), "workspace synctest "+deletedMsg) {
		t.Fatalf("sync of a deleted workspace: %v", err)
	}
	// Local reads refuse; the data is kept.
	if _, err := store.Open(p); err == nil || !strings.Contains(err.Error(), deletedMsg) {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := session.AppendFor(p, "", "done", "tester", "after"); !errors.Is(err, store.ErrWorkspaceDeleted) {
		t.Fatalf("session write: %v", err)
	}
	db, err := store.OpenAny(p)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM session_entries`).Scan(&n)
	db.Close()
	if n != 1 {
		t.Fatalf("local data: %d session entries", n)
	}
	// Another sync while still deleted: the same refusal, no crash.
	if _, err := Run(context.Background(), cl, p); !errors.Is(err, store.ErrWorkspaceDeleted) {
		t.Fatalf("second sync: %v", err)
	}

	// Restored elsewhere: the next sync goes through and clears the mark.
	f.mu.Lock()
	f.gone = false
	f.mu.Unlock()
	r, err := Run(context.Background(), cl, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.Notes, "|"), "live on gears-api again") {
		t.Fatalf("notes: %v", r.Notes)
	}
	if db, err := store.Open(p); err != nil {
		t.Fatalf("store.Open after restore: %v", err)
	} else {
		db.Close()
	}
}

// Card-only pull and the live board hear it too.
func TestCardsAndLiveWorkspaceDeleted(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := liveCache(t)
	f.SetWorkspace(p.ID, p.Slug)
	cl := api.Anonymous(srv.URL)

	// The stream says workspace.deleted: Live marks the cache and stops.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	var logs []string
	go func() {
		done <- Live(ctx, LiveOptions{Client: cl, Project: p, Backoff: 10 * time.Millisecond,
			Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	}()
	waitFor(t, 2*time.Second, "subscribed", func() bool { return f.Subscribers() == 1 })
	f.SetDeleted(true)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Live returned nil after workspace.deleted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Live still running after workspace.deleted")
	}
	if err := store.CheckLive(p); !errors.Is(err, store.ErrWorkspaceDeleted) {
		t.Fatalf("cache after the stream event: %v", err)
	}
	if !strings.Contains(strings.Join(logs, "|"), deletedMsg) {
		t.Fatalf("logs: %v", logs)
	}

	// Cleared by hand (as a restore does), a card pull hears 410 again.
	if _, err := store.ClearDeleted(p); err != nil {
		t.Fatal(err)
	}
	if _, err := PullCards(context.Background(), cl, p); !errors.Is(err, store.ErrWorkspaceDeleted) {
		t.Fatalf("PullCards: %v", err)
	}
	if err := store.CheckLive(p); !errors.Is(err, store.ErrWorkspaceDeleted) {
		t.Fatalf("cache after a 410 pull: %v", err)
	}
	// A stream that opens on a deleted workspace (410) stops at once.
	if _, err := store.ClearDeleted(p); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if err := Live(ctx2, LiveOptions{Client: cl, Project: p, Backoff: 10 * time.Millisecond}); err == nil || ctx2.Err() != nil {
		t.Fatalf("Live on a 410: %v (ctx %v)", err, ctx2.Err())
	}
	if err := store.CheckLive(p); !errors.Is(err, store.ErrWorkspaceDeleted) {
		t.Fatalf("cache after a 410 stream: %v", err)
	}
}
