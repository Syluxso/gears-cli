package mcpserver

import (
	"net/http/httptest"
	"testing"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/runs"
)

// GRS-72: runbook_history on the local MCP reads gears-api's timeline and
// caches it; with gears-api gone it answers the cached timeline, stale.
func TestRunbookHistoryLocal(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	me := connectAs(t, p, "claude-local")

	id := me.must("runbook_start", map[string]any{"runbook": "demo", "steps": []any{map[string]any{"id": "one"}, map[string]any{"id": "two"}}})["run"].(map[string]any)["id"].(string)
	me.must("runbook_step_complete", map[string]any{"run": id, "evidence": []any{map[string]any{"kind": "commit", "ref": "abcdef0"}}})
	me.must("runbook_stop", map[string]any{"run": id, "reason": "enough"})
	me.mustFail("runbook_history", map[string]any{"run": " "}, "run required")

	types := func(out map[string]any) []string {
		var got []string
		for _, e := range out["events"].([]any) {
			m := e.(map[string]any)
			got = append(got, m["type"].(string))
		}
		return got
	}
	want := []string{"run.started", "step.completed", "run.canceled"}
	out := me.must("runbook_history", map[string]any{"run": id})
	if got := types(out); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || out["status"] != "canceled" || out["stale"] != nil {
		t.Fatalf("history: %v", out)
	}
	evs, err := runs.Events(p, id)
	if err != nil || len(evs) != 3 || evs[2].Reason != "enough" {
		t.Fatalf("cached: %v %+v", err, evs)
	}

	srv.Close()
	off := me.must("runbook_history", map[string]any{"run": id[:12]})
	if got := types(off); len(got) != 3 || off["stale"] != true || off["note"] == nil {
		t.Fatalf("offline: %v", off)
	}
}
