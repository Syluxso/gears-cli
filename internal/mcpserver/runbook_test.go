package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/runs"
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

// GRS-56: the local MCP's runbook tools go to gears-api (never the
// orchestrator, never the retired local engine), take agent, write what
// they get back to the record cache (GRS-58), and sync pulls runs.
func TestRunbookToolsThroughAPI(t *testing.T) {
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

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, gone := range []string{"runbook_step_done", "runbook_skip", "runbook_takeover", "runbook_abandon"} {
		if have[gone] {
			t.Fatalf("%s is still served", gone)
		}
	}
	for _, want := range []string{"runbook_start", "runbook_step_complete", "runbook_step_skip", "runbook_status", "runbook_new", "runbook_list"} {
		if !have[want] {
			t.Fatalf("%s missing", want)
		}
	}

	started := must("runbook_start", map[string]any{"runbook": "gears-migrate", "cardKey": "WS-1", "agent": "claude-web"})
	run := started["run"].(map[string]any)
	id := run["id"].(string)
	if run["agentLabel"] != "claude-web" || run["currentStep"] != "a" || run["workspaceId"] != p.WorkspaceID() {
		t.Fatalf("start: %v", started)
	}
	if b := f.RunBodies()[0]; b["runbook"] != "gears-migrate" || b["cardKey"] != "WS-1" || b["agent"] != "claude-web" {
		t.Fatalf("start body: %v", b)
	}
	// Cached at once.
	if r, err := runs.Get(p, id); err != nil || r.CurrentStep != "a" {
		t.Fatalf("cache after start: %v %v", r, err)
	}

	// Without run: claude-local has none running (the run is claude-web's).
	mustFail("runbook_step_complete", map[string]any{}, "no running run")
	// Refused evidence leaves the step current.
	mustFail("runbook_step_complete", map[string]any{"run": id, "evidence": []any{map[string]any{"kind": "session", "ref": "missing"}}}, "evidence")
	out := must("runbook_step_complete", map[string]any{"agent": "claude-web", "note": "ok",
		"evidence": []any{map[string]any{"kind": "commit", "ref": "abc1234"}}})
	if out["stepId"] != "a" || out["currentStep"] != "b" {
		t.Fatalf("complete: %v", out)
	}
	if b := f.RunBodies()[len(f.RunBodies())-1]; b["agent"] != "claude-web" || b["note"] != "ok" {
		t.Fatalf("complete body: %v", b)
	}
	mustFail("runbook_step_skip", map[string]any{"run": id}, "reason")
	out = must("runbook_step_skip", map[string]any{"run": id[:12], "reason": "not here"})
	if out["runStatus"] != "completed" {
		t.Fatalf("skip: %v", out)
	}
	stat := must("runbook_status", map[string]any{"run": id})
	if stat["run"].(map[string]any)["status"] != "completed" {
		t.Fatalf("status: %v", stat)
	}
	list := must("runbook_status", map[string]any{})
	if len(list["runs"].([]any)) != 1 {
		t.Fatalf("status list: %v", list)
	}
	shown := must("runbook_show", map[string]any{"runbook": id})
	if shown["run"].(map[string]any)["status"] != "completed" {
		t.Fatalf("show: %v", shown)
	}
}
