package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A single-ticket run end to end, with two agents on one record: the owner
// rule, the card lock, evidence bound to the branch and start time, a
// when: skip, a gate that waits, and a takeover.
func TestSingleTicketRunOverMCP(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	oldSha := git(t, dir, "rev-parse", "HEAD")
	// Commit times are whole seconds; without this the run can start in the
	// same second as oldSha and the "predates this run" check cannot see it.
	time.Sleep(1100 * time.Millisecond)

	res, err := project.Init(dir, "rbtest", "RB")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	open := func(agent string) *mcp.ClientSession {
		srv := New(verbs.Ctx{Project: res.Project, Agent: agent}, "test")
		ct, st := mcp.NewInMemoryTransports()
		if _, err := srv.Connect(ctx, st, nil); err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: agent, Version: "0"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	alice, bob := open("alice"), open("bob")
	defer alice.Close()
	defer bob.Close()

	call := func(cs *mcp.ClientSession, name string, args map[string]any) (bool, string, map[string]any) {
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
	must := func(cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
		t.Helper()
		if isErr, text, out := call(cs, name, args); isErr {
			t.Fatalf("%s: %s", name, text)
		} else {
			return out
		}
		return nil
	}
	mustFail := func(cs *mcp.ClientSession, name string, args map[string]any, want string) {
		t.Helper()
		if isErr, text, _ := call(cs, name, args); !isErr || !strings.Contains(text, want) {
			t.Fatalf("%s: expected error containing %q, got err=%v %s", name, want, isErr, text)
		}
	}

	// Author the canonical runbook.
	must(alice, "runbook_new", map[string]any{"title": "Single ticket development", "target": "card"})
	add := func(args map[string]any) {
		args["runbook"] = "single-ticket-development"
		must(alice, "runbook_step_add", args)
	}
	add(map[string]any{"kind": "gears", "check": "card:In Progress", "text": "Take the card"})
	add(map[string]any{"kind": "git", "check": "branch", "text": "Branch from main, named after the card"})
	add(map[string]any{"kind": "do", "check": "commit", "text": "Implement; commit on the branch"})
	add(map[string]any{"kind": "gears", "check": "session", "text": "Log what was done"})
	add(map[string]any{"kind": "ask", "name": "review", "text": "Does this need a review? yes/no"})
	add(map[string]any{"kind": "gate", "check": "pushed", "when": "review=yes", "text": "Wait for the push"})
	add(map[string]any{"kind": "gears", "check": "card:Testing", "text": "Hand the card to testing"})
	mustFail(alice, "runbook_step_add", map[string]any{"runbook": "single-ticket-development", "kind": "gate", "text": "x"}, "gate needs a check")
	mustFail(alice, "runbook_step_add", map[string]any{"runbook": "single-ticket-development", "kind": "do", "check": "commit", "when": "nope=1", "text": "x"}, "no earlier step")

	// A card to run against.
	must(alice, "card_new", map[string]any{"title": "Runbooks v2", "acceptance": []string{"run completes, proven by runbook_show"},
		"story": map[string]any{"asA": "Darryn", "iWant": "runs that verify", "soThat": "agents cannot fake done"}})

	// Start binds the card and refuses a second run per agent or per card.
	run := must(alice, "runbook_start", map[string]any{"runbook": "single-ticket-development", "target": "RB-1"})
	runID := run["id"].(string)
	if run["card"] != "RB-1" {
		t.Fatalf("run card: %v", run["card"])
	}
	mustFail(alice, "runbook_start", map[string]any{"runbook": "single-ticket-development", "target": "RB-1"}, "already has an active run")
	mustFail(bob, "runbook_start", map[string]any{"runbook": "single-ticket-development", "target": "RB-1"}, "already has an active run")
	// The run owns the card.
	mustFail(bob, "card_move", map[string]any{"key": "RB-1", "state": "Testing"}, "driven by run")
	mustFail(bob, "card_claim", map[string]any{"key": "RB-1"}, "driven by run")
	// Only alice advances it.
	mustFail(bob, "runbook_step_done", map[string]any{"run": runID, "evidence": "x"}, "belongs to alice")

	// Step 1: gears card:In Progress moves the card.
	out := must(alice, "runbook_step_done", map[string]any{})
	if out["verified"] != true {
		t.Fatalf("step 1: %v", out)
	}
	card := must(alice, "card_show", map[string]any{"key": "RB-1"})
	if card["state"] != "In Progress" || card["assignee"] != "alice" || card["run"] == nil {
		t.Fatalf("card after step 1: state=%v assignee=%v run=%v", card["state"], card["assignee"], card["run"])
	}

	// Step 2: branch must name the card.
	git(t, dir, "checkout", "-q", "-b", "wrong-name")
	mustFail(alice, "runbook_step_done", map[string]any{}, "does not name the card")
	git(t, dir, "checkout", "-q", "-b", "rb-1-runbooks-v2")
	out = must(alice, "runbook_step_done", map[string]any{})
	if out["verified"] != true {
		t.Fatalf("branch step: %v", out)
	}

	// Step 3: a commit from before the run is refused; a new one on the branch passes.
	mustFail(alice, "runbook_step_done", map[string]any{"evidence": oldSha}, "predates this run")
	os.WriteFile(filepath.Join(dir, "work.txt"), []byte("done\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "work")
	out = must(alice, "runbook_step_done", map[string]any{})
	if out["verified"] != true || !strings.Contains(out["note"].(string), "rb-1-runbooks-v2") {
		t.Fatalf("commit step: %v", out)
	}

	// Step 4: the session step logs the text itself.
	out = must(alice, "runbook_step_done", map[string]any{"evidence": "Implemented the thing"})
	if ev := out["step"].(map[string]any)["evidence"].(string); len(ev) != 36 || strings.Count(ev, "-") != 4 {
		t.Fatalf("session step: %v", out)
	}

	// Step 5: ask, answered no, so the pushed gate is skipped by when:.
	out = must(alice, "runbook_step_done", map[string]any{"evidence": "no"})
	next := out["next"].(map[string]any)
	if next["idx"] != float64(7) {
		t.Fatalf("expected gate skipped and step 7 current, got next %v", next)
	}

	// Bob takes over with a reason and finishes.
	mustFail(bob, "runbook_takeover", map[string]any{"run": runID, "reason": " "}, "reason is required")
	must(bob, "runbook_takeover", map[string]any{"run": runID, "reason": "alice went home"})
	mustFail(alice, "runbook_step_done", map[string]any{}, "belongs to bob")
	out = must(bob, "runbook_step_done", map[string]any{})
	if out["runFinished"] != true {
		t.Fatalf("expected run finished: %v", out)
	}
	card = must(bob, "card_show", map[string]any{"key": "RB-1"})
	if card["state"] != "Testing" || card["assignee"] != "bob" || card["run"] != nil {
		t.Fatalf("card after run: %v %v %v", card["state"], card["assignee"], card["run"])
	}
	shown := must(bob, "runbook_show", map[string]any{"key": runID})
	r := shown["run"].(map[string]any)
	if r["status"] != "completed" || r["branch"] != "rb-1-runbooks-v2" {
		t.Fatalf("run: %v", r)
	}
	steps := r["steps"].([]any)
	if s6 := steps[5].(map[string]any); s6["state"] != "skipped" || !strings.Contains(s6["reason"].(string), "review=yes") {
		t.Fatalf("gate step: %v", s6)
	}
	// Card is unlocked again and the trail shows the run.
	must(bob, "card_move", map[string]any{"key": "RB-1", "state": "Completed"})
	events := card["events"].([]any)
	found := false
	for _, e := range events {
		if e.(map[string]any)["kind"] == "run" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no run events on card: %v", events)
	}
}
