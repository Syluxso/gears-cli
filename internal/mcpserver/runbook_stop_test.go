package mcpserver

import (
	"net/http/httptest"
	"testing"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/runs"
)

// GRS-69: runbook_stop on the local MCP goes to gears-api's cancel, and the
// canceled run lands in the cache (status, reason, the canceled step).
func TestRunbookStopLocal(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	me := connectAs(t, p, "claude-local")

	id := me.must("runbook_start", map[string]any{"runbook": "demo", "steps": []any{map[string]any{"id": "one"}, map[string]any{"id": "two"}}})["run"].(map[string]any)["id"].(string)
	me.mustFail("runbook_stop", map[string]any{"run": id}, "reason")
	me.mustFail("runbook_stop", map[string]any{"reason": "x"}, "run")
	me.mustFail("runbook_stop", map[string]any{"run": " ", "reason": "x"}, "run required")
	out := me.must("runbook_stop", map[string]any{"run": id[:12], "reason": "wrong runbook"})
	if out["status"] != "canceled" || out["cancelReason"] != "wrong runbook" || out["canceledLabel"] != "claude-local" {
		t.Fatalf("stop: %v", out)
	}
	bodies := f.RunBodies()
	if last := bodies[len(bodies)-1]; last["reason"] != "wrong runbook" || last["agent"] != "claude-local" {
		t.Fatalf("cancel body: %v", last)
	}
	me.mustFail("runbook_stop", map[string]any{"run": id, "reason": "again"}, "canceled")

	cached, err := runs.Get(p, id)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Status != "canceled" || cached.CancelReason != "wrong runbook" || cached.CanceledAt == nil || cached.Steps[0].Status != "canceled" {
		t.Fatalf("cache: %+v", cached)
	}
	if r := me.must("runbook_runs", map[string]any{"status": "canceled"})["runs"].([]any); len(r) != 1 {
		t.Fatalf("canceled runs: %v", r)
	}
}
