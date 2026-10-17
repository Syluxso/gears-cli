package mcpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
)

// GRS-87: runbook_next on the local MCP asks gears-api what to do now; a
// system step waits on the system, an approval on a person, and both refuse
// an agent's complete. Offline it answers the cached run, stale.
func TestRunbookNextLocal(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	me := connectAs(t, p, "claude-local")

	me.mustFail("runbook_next", map[string]any{}, "no running run")
	id := me.must("runbook_start", map[string]any{"runbook": "audit", "steps": []any{
		map[string]any{"id": "take", "kind": "card.move", "params": map[string]any{"state": "Testing"}},
		map[string]any{"id": "work", "kind": "do"},
		map[string]any{"id": "ok", "kind": "approval"},
	}})["run"].(map[string]any)["id"].(string)

	next := me.must("runbook_next", map[string]any{})
	if next["runId"] != id || next["waitingOn"] != "system" || next["step"].(map[string]any)["executor"] != "system" || next["progress"] != "0/3" {
		t.Fatalf("next: %v", next)
	}
	me.mustFail("runbook_step_complete", map[string]any{"run": id, "evidence": []any{map[string]any{"kind": "card", "ref": "WS-1"}}}, "the system does this step")
	me.must("runbook_step_skip", map[string]any{"run": id, "reason": "system is down"})
	next = me.must("runbook_next", map[string]any{"run": id})
	if next["waitingOn"] != "agent" || len(next["next"].([]any)) != 1 {
		t.Fatalf("next agent: %v", next)
	}
	me.must("runbook_step_complete", map[string]any{"run": id, "evidence": []any{map[string]any{"kind": "commit", "ref": "abcdef0"}}})
	next = me.must("runbook_next", map[string]any{"run": id})
	if next["waitingOn"] != "human" || !strings.Contains(next["instructions"].(string), "Do not complete it") {
		t.Fatalf("next human: %v", next)
	}
	me.mustFail("runbook_step_complete", map[string]any{"run": id, "evidence": []any{map[string]any{"kind": "desk", "ref": "x"}}}, "needs a person")

	// Two running runs: listed to pick from.
	me.must("runbook_start", map[string]any{"runbook": "other", "steps": []any{map[string]any{"id": "x"}}})
	pick := me.must("runbook_next", map[string]any{})
	if len(pick["runs"].([]any)) != 2 || !strings.Contains(pick["message"].(string), "which one") {
		t.Fatalf("pick: %v", pick)
	}

	// GRS-88: an agent never answers a run's approval item.
	d := me.must("desk_send", map[string]any{"type": "ask", "text": "Runbook audit is waiting on step ok (approval): ok", "ref": id + "#ok"})
	me.mustFail("desk_answer", map[string]any{"id": d["id"], "answer": "yes"}, "needs a person")

	srv.Close()
	off := me.must("runbook_next", map[string]any{"run": id[:12]})
	if off["stale"] != true || off["waitingOn"] != "human" {
		t.Fatalf("offline: %v", off)
	}
}
