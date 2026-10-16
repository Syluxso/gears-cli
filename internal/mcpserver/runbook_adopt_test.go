package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/runs"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-59: official runbooks adopted through gears-api. A frozen copy is
// refused by runbook_edit and by every local authoring tool; an editable
// copy changes with runbook_edit; both start by slug.
func TestRunbookAdoptTools(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)

	ctx := context.Background()
	s := New(verbs.Ctx{Project: p, Agent: "claude-local"}, "test")
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) (bool, string, map[string]any) {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := r.Content[0].(*mcp.TextContent).Text
		var out map[string]any
		_ = json.Unmarshal([]byte(text), &out)
		return r.IsError, text, out
	}
	must := func(name string, args map[string]any) map[string]any {
		t.Helper()
		isErr, text, out := call(name, args)
		if isErr {
			t.Fatalf("%s: %s", name, text)
		}
		return out
	}
	mustFail := func(name string, args map[string]any, want string) {
		t.Helper()
		if isErr, text, _ := call(name, args); !isErr || !strings.Contains(text, want) {
			t.Fatalf("%s: want error with %q, got %v %s", name, want, isErr, text)
		}
	}

	cat := must("runbook_catalog", map[string]any{})
	if len(cat["official"].([]any)) != 2 || len(cat["adopted"].([]any)) != 0 {
		t.Fatalf("catalog: %v", cat)
	}
	mustFail("runbook_adopt", map[string]any{"ref": "gears-migrate"}, "no official runbook")
	fz := must("runbook_adopt", map[string]any{"ref": "single-ticket-development"})
	if fz["frozen"] != true || fz["slug"] != "single-ticket-development" {
		t.Fatalf("adopt frozen: %v", fz)
	}
	if rb, _ := runs.Runbook(p, "single-ticket-development"); rb == nil || !rb.Frozen {
		t.Fatalf("not cached: %v", rb)
	}

	// Frozen: refused by runbook_edit and every local authoring tool, with
	// the same clear words.
	mustFail("runbook_edit", map[string]any{"runbook": "single-ticket-development", "title": "x"}, "frozen copy")
	for name, args := range map[string]map[string]any{
		"runbook_step_add":    {"runbook": "single-ticket-development", "kind": "do", "text": "x"},
		"runbook_step_update": {"runbook": "single-ticket-development", "step": 1, "text": "x"},
		"runbook_step_remove": {"runbook": "single-ticket-development", "step": 1},
		"runbook_step_move":   {"runbook": "single-ticket-development", "step": 1, "to": 2},
		"runbook_update":      {"runbook": "single-ticket-development", "title": "x"},
		"runbook_delete":      {"runbook": "single-ticket-development"},
	} {
		mustFail(name, args, "frozen copy of the official runbook single-ticket-development")
	}

	// Editable: the workspace's own copy.
	ed := must("runbook_adopt", map[string]any{"ref": "starter"})
	if ed["slug"] != "starter-copy" || ed["frozen"] != false {
		t.Fatalf("adopt editable: %v", ed)
	}
	mustFail("runbook_step_add", map[string]any{"runbook": "starter-copy", "kind": "do", "text": "x"}, "runbook_edit")
	edited := must("runbook_edit", map[string]any{"runbook": "starter-copy", "title": "Ours",
		"steps": []any{map[string]any{"id": "plan", "kind": "ask"}, map[string]any{"id": "ship", "kind": "do"}, map[string]any{"id": "log", "kind": "note"}}})
	if edited["title"] != "Ours" || len(edited["steps"].([]any)) != 3 {
		t.Fatalf("edit: %v", edited)
	}
	if rb, _ := runs.Runbook(p, "starter-copy"); rb == nil || len(rb.Steps) != 3 {
		t.Fatalf("edit not cached: %v", rb)
	}

	list := must("runbook_list", map[string]any{})
	if len(list["adopted"].([]any)) != 2 {
		t.Fatalf("list adopted: %v", list)
	}
	shown := must("runbook_show", map[string]any{"key": "starter-copy"})
	if shown["adopted"].(map[string]any)["title"] != "Ours" {
		t.Fatalf("show: %v", shown)
	}

	// Start by slug: gears-api resolves the copy.
	started := must("runbook_start", map[string]any{"runbook": "single-ticket-development"})
	if started["run"].(map[string]any)["runbook"] != "single-ticket-development" {
		t.Fatalf("start: %v", started)
	}

	// Sync pulls the copies into a fresh cache.
	must("sync", map[string]any{})
	all, err := runs.Runbooks(p)
	if err != nil || len(all) != 2 {
		t.Fatalf("after sync: %v %v", all, err)
	}
}
