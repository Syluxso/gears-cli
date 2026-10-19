package verbs

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/store"
)

// GRS-92: gears runbook next --hook. Every branch: block only when a run
// this label owns waits on the agent; otherwise nothing.

func hookStart(t *testing.T, c Ctx, name string, steps ...api.StepDef) string {
	t.Helper()
	out, err := RunbookStart(c, RunbookStartIn{Runbook: name, Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	return out.Run.ID
}

func mustAllow(t *testing.T, c Ctx, in StopHookIn, want string) {
	t.Helper()
	out, why := StopHook(c, in, 2*time.Second)
	if out != nil {
		t.Fatalf("want allow (%s), got block: %s", want, out.Reason)
	}
	if !strings.Contains(why, want) {
		t.Fatalf("allow for the wrong reason: %q, want %q", why, want)
	}
}

func mustBlock(t *testing.T, c Ctx, in StopHookIn) *StopHookOut {
	t.Helper()
	out, why := StopHook(c, in, 2*time.Second)
	if out == nil || out.Decision != "block" {
		t.Fatalf("want block, got allow: %s", why)
	}
	return out
}

func TestStopHookBranches(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	me := Ctx{Project: p, Agent: "claude-local"}
	other := Ctx{Project: p, Agent: "grok-cli"}
	in := StopHookIn{SessionID: "s-1", HookEventName: "Stop"}

	// Not in a workspace; no run.
	mustAllow(t, Ctx{Agent: "claude-local"}, in, "not in a Gears workspace")
	mustAllow(t, me, in, "owns no running run")

	// A run someone else owns does not keep me going.
	id := hookStart(t, other, "demo", api.StepDef{ID: "work", Title: "Write the code"}, api.StepDef{ID: "ship"})
	mustAllow(t, me, in, "owns no running run")
	got := mustBlock(t, other, StopHookIn{SessionID: "s-other"})
	if !strings.Contains(got.Reason, "Gears run demo ("+id[:12]+") is waiting on you: step work — work") ||
		!strings.Contains(got.Reason, "runbook_step_complete with evidence; or runbook_step_skip with a reason; stop by telling the user why") {
		t.Fatalf("reason: %s", got.Reason)
	}

	// Claimed: now it keeps me going, and not the starter.
	if _, err := RunbookClaim(me, RunbookClaimIn{Run: id}); err != nil {
		t.Fatal(err)
	}
	mustAllow(t, other, StopHookIn{SessionID: "s-other"}, "owns no running run")
	mustBlock(t, me, in)

	// Already continuing because of the hook and the run has not moved:
	// let it stop. A stop that is not a continuation blocks again.
	mustAllow(t, me, StopHookIn{SessionID: "s-1", StopHookActive: true}, "has not moved")
	mustBlock(t, me, in)
	// Another session is its own loop.
	mustBlock(t, me, StopHookIn{SessionID: "s-2", StopHookActive: true})
	// The step moved: continuing is progress, block with the new step.
	if _, err := RunbookStepComplete(me, RunbookStepCompleteIn{RunPick: RunPick{Run: id}, Evidence: []api.Evidence{{Kind: "commit", Ref: "abcdef0"}}}); err != nil {
		t.Fatal(err)
	}
	got = mustBlock(t, me, StopHookIn{SessionID: "s-1", StopHookActive: true})
	if !strings.Contains(got.Reason, "step ship") {
		t.Fatalf("moved: %s", got.Reason)
	}
	raw, err := os.ReadFile(filepath.Join(p.GearboxPath(), "stop-hook.json"))
	if err != nil || !strings.Contains(string(raw), `"stepId": "ship"`) {
		t.Fatalf("state file: %s %v", raw, err)
	}

	// Finished: nothing.
	if _, err := RunbookStepComplete(me, RunbookStepCompleteIn{RunPick: RunPick{Run: id}, Evidence: []api.Evidence{{Kind: "commit", Ref: "abcdef0"}}}); err != nil {
		t.Fatal(err)
	}
	mustAllow(t, me, in, "owns no running run")

	// Waiting on the system, then on a person: nothing.
	sys := hookStart(t, me, "sys", api.StepDef{ID: "take", Kind: "card.move", Params: map[string]any{"state": "Testing"}}, api.StepDef{ID: "ok", Kind: "approval"})
	mustAllow(t, me, in, "no run owned by claude-local waits on the agent")
	if _, err := RunbookStepSkip(me, RunbookStepSkipIn{RunPick: RunPick{Run: sys}, Reason: "system down"}); err != nil {
		t.Fatal(err)
	}
	mustAllow(t, me, in, "no run owned by claude-local waits on the agent")

	// Several owned runs: the most recently updated one waiting on me.
	time.Sleep(5 * time.Millisecond)
	a := hookStart(t, me, "first", api.StepDef{ID: "one"})
	time.Sleep(5 * time.Millisecond)
	b := hookStart(t, me, "second", api.StepDef{ID: "two"})
	got = mustBlock(t, me, StopHookIn{SessionID: "s-3"})
	if !strings.Contains(got.Reason, b[:12]) || strings.Contains(got.Reason, a[:12]) {
		t.Fatalf("most recent: %s", got.Reason)
	}

	// Slow gears-api: nothing, within the budget.
	f.RunsDelay = 3 * time.Second
	start := time.Now()
	if out, why := StopHook(me, in, 300*time.Millisecond); out != nil || !strings.Contains(why, "allow") {
		t.Fatalf("slow: %v %s", out, why)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("slow hook took %s", time.Since(start))
	}
	f.RunsDelay = 0

	// Deleted workspace: nothing.
	if err := store.MarkDeleted(p, "ws"); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("mark: %v", err)
	}
	mustAllow(t, me, in, "was deleted")
	if _, err := store.ClearDeleted(p); err != nil {
		t.Fatal(err)
	}
	mustBlock(t, me, StopHookIn{SessionID: "s-4"})

	// gears-api offline: nothing.
	srv.Close()
	mustAllow(t, me, StopHookIn{SessionID: "s-5"}, "unreachable")
}

func TestStopHookNotLoggedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	p := fakeapi.Workspace(t)
	mustAllow(t, Ctx{Project: p, Agent: "claude-local"}, StopHookIn{SessionID: "x"}, "gears login")
}

// GRS-100: at a card.wait the hook asks gears-api: while audit tickets
// are open the agent is kept working; once none are, it may stop.
func TestStopHookCardWait(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	me := Ctx{Project: fakeapi.Workspace(t), Agent: "claude-local"}
	hookStart(t, me, "audit", api.StepDef{ID: "wait", Kind: "card.wait", Title: "Wait for the audit tickets", Params: map[string]any{"open": true}})
	if got := mustBlock(t, me, StopHookIn{SessionID: "s-wait"}); !strings.Contains(got.Reason, "work the open cards") {
		t.Fatalf("reason: %s", got.Reason)
	}
	me2 := Ctx{Project: fakeapi.Workspace(t), Agent: "grok-cli"}
	hookStart(t, me2, "audit", api.StepDef{ID: "wait", Kind: "card.wait", Title: "Wait", Params: map[string]any{"open": false}})
	mustAllow(t, me2, StopHookIn{SessionID: "s-done"}, "waits on the agent")
}
