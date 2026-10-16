package cli

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/store"
)

// GRS-80: gears workspace delete / restore / list --deleted, and every
// other command refusing while the workspace is deleted.
func TestWorkspaceCommands(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	t.Setenv("GEARS_CARD_SYNC", "off")
	t.Setenv("GEARS_AGENT", "tester")
	p := fakeapi.Workspace(t)
	f.SetWorkspace(p.ID, p.Slug)
	t.Cleanup(func() { wsConfirm, wsDeleteYes, wsListDeleted = "", false, false })

	if err := runIn(t, p.Root, "workspace", "delete", "--confirm", "wrong"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong confirm: %v", err)
	}
	wsConfirm = ""
	// Not a terminal and no --confirm / --yes: refused before the API.
	if err := runIn(t, p.Root, "workspace", "delete"); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("no confirm: %v", err)
	}
	if err := runIn(t, p.Root, "workspace", "delete", "--yes", "--json"); err != nil {
		t.Fatalf("delete --yes: %v", err)
	}
	wsDeleteYes = false
	if err := store.CheckLive(p); !errors.Is(err, store.ErrWorkspaceDeleted) {
		t.Fatalf("cache not marked: %v", err)
	}
	for _, args := range [][]string{{"status"}, {"card", "list"}, {"desk", "list"}, {"session", "list"}, {"artifact", "list"}} {
		if err := runIn(t, p.Root, args...); !errors.Is(err, store.ErrWorkspaceDeleted) ||
			!strings.Contains(err.Error(), "restore: gears workspace restore") {
			t.Fatalf("%v while deleted: %v", args, err)
		}
	}
	if err := runIn(t, p.Root, "workspace", "list", "--deleted", "--json"); err != nil {
		t.Fatalf("list --deleted: %v", err)
	}
	wsListDeleted = false
	if err := runIn(t, p.Root, "workspace", "restore", p.Slug, "--json"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := store.CheckLive(p); err != nil {
		t.Fatalf("cache after restore: %v", err)
	}
	if err := runIn(t, p.Root, "status", "--json"); err != nil {
		t.Fatalf("status after restore: %v", err)
	}
}
