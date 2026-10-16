package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// toolCaller is an in-memory MCP client of New for agent.
type toolCaller struct {
	t  *testing.T
	cs *mcp.ClientSession
}

func connectAs(t *testing.T, p *project.Project, agent string) *toolCaller {
	t.Helper()
	ctx := context.Background()
	s := New(verbs.Ctx{Project: p, Agent: agent}, "test")
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &toolCaller{t: t, cs: cs}
}

func (c *toolCaller) call(name string, args map[string]any) (bool, string, map[string]any) {
	c.t.Helper()
	r, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		c.t.Fatalf("%s: %v", name, err)
	}
	text := r.Content[0].(*mcp.TextContent).Text
	var out map[string]any
	_ = json.Unmarshal([]byte(text), &out)
	return r.IsError, text, out
}

func (c *toolCaller) must(name string, args map[string]any) map[string]any {
	c.t.Helper()
	isErr, text, out := c.call(name, args)
	if isErr {
		c.t.Fatalf("%s: %s", name, text)
	}
	return out
}

func (c *toolCaller) mustFail(name string, args map[string]any, want string) {
	c.t.Helper()
	if isErr, text, _ := c.call(name, args); !isErr || !strings.Contains(text, want) {
		c.t.Fatalf("%s: want error with %q, got %v %s", name, want, isErr, text)
	}
}

// GRS-71: runbook_runs (filters, mine, paging), and every run read works
// when gears-api has no orchestrator: runbook_runs, runbook_status (stale
// on refresh), runbook_list (adopted + note), status's myRuns; writes 501.
func TestRunReadsWithoutOrchestrator(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	me := connectAs(t, p, "claude-local")
	other := connectAs(t, p, "grok-web")

	r1 := me.must("runbook_start", map[string]any{"runbook": "demo", "steps": []any{map[string]any{"id": "one"}, map[string]any{"id": "two"}}})["run"].(map[string]any)["id"].(string)
	me.must("runbook_step_complete", map[string]any{"run": r1, "evidence": []any{map[string]any{"kind": "commit", "ref": "abcdef0"}}})
	other.must("runbook_start", map[string]any{"runbook": "starter-copy", "card": "WS-1"})
	me.must("runbook_adopt", map[string]any{"ref": "starter"})

	all := me.must("runbook_runs", map[string]any{})
	if len(all["runs"].([]any)) != 2 {
		t.Fatalf("all: %v", all)
	}
	mine := me.must("runbook_runs", map[string]any{"mine": true})
	runs := mine["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("mine: %v", mine)
	}
	m := runs[0].(map[string]any)
	if m["id"] != r1 || m["progress"] != "1/2" || m["currentStep"].(map[string]any)["id"] != "two" || m["agent"] != "claude-local" {
		t.Fatalf("summary: %v", m)
	}
	if r := me.must("runbook_runs", map[string]any{"by": "grok-web", "runbook": "starter-copy", "card": "ws-1", "status": "active"})["runs"].([]any); len(r) != 1 {
		t.Fatalf("filters: %v", r)
	}
	p1 := me.must("runbook_runs", map[string]any{"limit": 1})
	if len(p1["runs"].([]any)) != 1 || p1["next"] == nil {
		t.Fatalf("page: %v", p1)
	}
	if p2 := me.must("runbook_runs", map[string]any{"limit": 1, "cursor": p1["next"]}); len(p2["runs"].([]any)) != 1 || p2["next"] != nil {
		t.Fatalf("page 2: %v", p2)
	}

	// The orchestrator gone: reads answer from the record.
	f.RunbooksOff = true
	defer func() { f.RunbooksOff = false }()
	if r := me.must("runbook_runs", map[string]any{"mine": true})["runs"].([]any); len(r) != 1 {
		t.Fatalf("runs, off: %v", r)
	}
	st := me.must("runbook_status", map[string]any{"run": r1})
	if st["run"].(map[string]any)["currentStep"] != "two" || st["stale"] != nil {
		t.Fatalf("status, off: %v", st)
	}
	st = me.must("runbook_status", map[string]any{"run": r1, "refresh": true})
	if st["stale"] != true || !strings.Contains(st["note"].(string), "not refreshed") {
		t.Fatalf("status refresh, off: %v", st)
	}
	list := me.must("runbook_list", map[string]any{})
	if list["officialAvailable"] != false || len(list["adopted"].([]any)) != 1 || !strings.Contains(list["note"].(string), "runbook service") {
		t.Fatalf("list, off: %v", list)
	}
	stat := me.must("status", map[string]any{})
	my := stat["myRuns"].([]any)
	if len(my) != 1 || my[0].(map[string]any)["current"].(map[string]any)["text"] != "two" || len(stat["runs"].([]any)) != 2 {
		t.Fatalf("status myRuns: %v %v", stat["myRuns"], stat["runs"])
	}
	me.mustFail("runbook_step_skip", map[string]any{"run": r1, "reason": "x"}, "not available")
	me.mustFail("runbook_start", map[string]any{"runbook": "demo"}, "not available")

	// gears-api offline: the cache, stale.
	srv.Close()
	off := me.must("runbook_runs", map[string]any{"mine": true})
	if off["stale"] != true || len(off["runs"].([]any)) != 1 {
		t.Fatalf("runs offline: %v", off)
	}
	if l := me.must("runbook_list", map[string]any{}); l["stale"] != true || len(l["adopted"].([]any)) != 1 {
		t.Fatalf("list offline: %v", l)
	}
}
