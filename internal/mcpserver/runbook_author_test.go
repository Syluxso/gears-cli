package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// Authoring beyond append over MCP: copy a built-in, edit steps in place,
// remove, move, rename, retarget, delete; every edit revalidated; a runbook
// under an active run is frozen; a copy from another project directory.
func TestRunbookAuthoringOverMCP(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")

	res, err := project.Init(dir, "authtest", "AU")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	srv := New(verbs.Ctx{Project: res.Project, Agent: "alice"}, "test")
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "alice", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	call := func(name string, args map[string]any) (bool, string, map[string]any) {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := ""
		if len(r.Content) > 0 {
			if tc, ok := r.Content[0].(*mcp.TextContent); ok {
				text = tc.Text
			}
		}
		var out map[string]any
		if !r.IsError {
			_ = json.Unmarshal([]byte(text), &out)
		}
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
			t.Fatalf("%s: expected error containing %q, got err=%v %s", name, want, isErr, text)
		}
	}
	steps := func(out map[string]any) []map[string]any {
		raw := out["steps"].([]any)
		var ss []map[string]any
		for _, x := range raw {
			ss = append(ss, x.(map[string]any))
		}
		return ss
	}
	texts := func(out map[string]any) string {
		var ts []string
		for _, s := range steps(out) {
			ts = append(ts, s["text"].(string))
		}
		return strings.Join(ts, " | ")
	}

	// The list names the built-ins even when the project has none.
	list := must("runbook_list", map[string]any{})
	if b := list["builtins"].([]any); len(b) < 1 {
		t.Fatalf("no builtins listed: %v", list)
	}

	// Copy the canonical built-in.
	mustFail("runbook_copy", map[string]any{"runbook": "nope"}, "no built-in runbook")
	rb := must("runbook_copy", map[string]any{"runbook": "single-ticket-development"})
	if rb["target"] != "card" || len(steps(rb)) != 7 {
		t.Fatalf("copied runbook: %v", rb)
	}
	mustFail("runbook_copy", map[string]any{"runbook": "single-ticket-development"}, "already exists")
	// Copy again under another name.
	rb2 := must("runbook_copy", map[string]any{"runbook": "single-ticket-development", "as": "Ticket flow"})
	if rb2["slug"] != "ticket-flow" || rb2["title"] != "Ticket flow" {
		t.Fatalf("renamed copy: %v", rb2)
	}
	const slug = "ticket-flow"

	// Update text and check in place.
	rb = must("runbook_step_update", map[string]any{"runbook": slug, "step": 3, "text": "Implement and commit", "check": "commit"})
	if s := steps(rb)[2]; s["text"] != "Implement and commit" || s["check"] != "commit" {
		t.Fatalf("step 3 after update: %v", s)
	}
	// Bad check for the kind is refused; the file is unchanged.
	mustFail("runbook_step_update", map[string]any{"runbook": slug, "step": 3, "check": "pushed"}, "for kind do")
	// Renaming the ask that the gate refers to is refused.
	mustFail("runbook_step_update", map[string]any{"runbook": slug, "step": 5, "name": "needs-review"}, "refer to it")
	// Removing it likewise.
	mustFail("runbook_step_remove", map[string]any{"runbook": slug, "step": 5}, "refer to it")
	// Clear the gate's when, then the rename goes through.
	must("runbook_step_update", map[string]any{"runbook": slug, "step": 6, "when": ""})
	rb = must("runbook_step_update", map[string]any{"runbook": slug, "step": 5, "name": "needs-review"})
	if steps(rb)[4]["name"] != "needs-review" {
		t.Fatalf("rename: %v", steps(rb)[4])
	}
	// A when on the new name is accepted; a when on the old name is not.
	mustFail("runbook_step_update", map[string]any{"runbook": slug, "step": 6, "when": "review=yes"}, "no earlier step")
	must("runbook_step_update", map[string]any{"runbook": slug, "step": 6, "when": "needs-review=yes"})

	// Move: the gate cannot go before the ask it depends on.
	mustFail("runbook_step_move", map[string]any{"runbook": slug, "step": 6, "to": 4}, "break a when")
	// Moving the session log step to the end is fine.
	rb = must("runbook_step_move", map[string]any{"runbook": slug, "step": 4, "to": 7})
	if got := texts(rb); !strings.HasSuffix(got, "Hand the card to testing | Log what was done") {
		t.Fatalf("after move: %s", got)
	}
	mustFail("runbook_step_move", map[string]any{"runbook": slug, "step": 9, "to": 1}, "has 7 step")

	// Order now: take, branch, implement, ask, gate, testing, log. The ask
	// is still referenced by the gate; remove the gate first, then the ask.
	mustFail("runbook_step_remove", map[string]any{"runbook": slug, "step": 4}, "refer to it")
	must("runbook_step_remove", map[string]any{"runbook": slug, "step": 5})
	rb = must("runbook_step_remove", map[string]any{"runbook": slug, "step": 4})
	if got := texts(rb); got != "Take the card | Branch from main, named after the card | Implement and commit | Hand the card to testing | Log what was done" {
		t.Fatalf("after removes: %s", got)
	}

	// Title and target.
	rb = must("runbook_update", map[string]any{"runbook": slug, "title": "Ticket flow v2"})
	if rb["title"] != "Ticket flow v2" || rb["slug"] != slug {
		t.Fatalf("update: %v", rb)
	}
	mustFail("runbook_update", map[string]any{"runbook": slug, "target": "repo"}, "needs a runbook with target card")
	mustFail("runbook_update", map[string]any{"runbook": slug, "target": "bogus"}, "one of card, repo, none")

	// Frozen while a run is active.
	must("card_new", map[string]any{"title": "Freeze me", "acceptance": []string{"frozen, proven by test"},
		"story": map[string]any{"asA": "a", "iWant": "b", "soThat": "c"}})
	run := must("runbook_start", map[string]any{"runbook": slug, "target": "AU-1"})
	mustFail("runbook_step_update", map[string]any{"runbook": slug, "step": 1, "text": "x"}, "in use by active run")
	mustFail("runbook_step_remove", map[string]any{"runbook": slug, "step": 1}, "in use by active run")
	mustFail("runbook_step_move", map[string]any{"runbook": slug, "step": 1, "to": 2}, "in use by active run")
	mustFail("runbook_update", map[string]any{"runbook": slug, "title": "x"}, "in use by active run")
	mustFail("runbook_delete", map[string]any{"runbook": slug}, "in use by active run")
	// The other runbook is still editable.
	must("runbook_update", map[string]any{"runbook": "single-ticket-development", "title": "STD"})
	must("runbook_abandon", map[string]any{"run": run["id"], "reason": "test over"})

	// Delete, and the finished run still reads.
	del := must("runbook_delete", map[string]any{"runbook": slug})
	if del["deleted"] != true {
		t.Fatalf("delete: %v", del)
	}
	mustFail("runbook_show", map[string]any{"key": slug}, "no runbook")
	shown := must("runbook_show", map[string]any{"key": run["id"]})
	if shown["run"].(map[string]any)["status"] != "abandoned" {
		t.Fatalf("run after delete: %v", shown)
	}

	// Copy from another project directory.
	other := t.TempDir()
	if _, err := project.Init(other, "othertest", "OT"); err != nil {
		t.Fatal(err)
	}
	osrv := New(verbs.Ctx{Project: mustProject(t, other), Agent: "alice"}, "test")
	oct, ost := mcp.NewInMemoryTransports()
	if _, err := osrv.Connect(ctx, ost, nil); err != nil {
		t.Fatal(err)
	}
	ocs, err := mcp.NewClient(&mcp.Implementation{Name: "alice", Version: "0"}, nil).Connect(ctx, oct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ocs.Close()
	r, err := ocs.CallTool(ctx, &mcp.CallToolParams{Name: "runbook_copy", Arguments: map[string]any{"runbook": "single-ticket-development", "from": dir}})
	if err != nil || r.IsError {
		t.Fatalf("copy from dir: %v %v", err, r.Content)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &got)
	if got["title"] != "STD" || len(got["steps"].([]any)) != 7 {
		t.Fatalf("copied from dir: %v", got)
	}
	r, _ = ocs.CallTool(ctx, &mcp.CallToolParams{Name: "runbook_copy", Arguments: map[string]any{"runbook": "nothing-here", "from": dir}})
	if !r.IsError {
		t.Fatalf("expected failure copying a missing runbook")
	}
}

func mustProject(t *testing.T, dir string) *project.Project {
	t.Helper()
	p, err := project.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
