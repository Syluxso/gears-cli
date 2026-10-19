package mcpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
)

// GRS-91: a run is owned by its starter until runbook_claim moves it; the
// default run of runbook_next and the step tools is the one you own, and
// runbook_runs filters by owner (offline too, from the cache).
func TestRunbookClaimLocal(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	me := connectAs(t, p, "claude-local")
	other := connectAs(t, p, "grok-cli")

	id := other.must("runbook_start", map[string]any{"runbook": "demo", "steps": []any{map[string]any{"id": "work"}, map[string]any{"id": "more"}}})["run"].(map[string]any)["id"].(string)
	if r := other.must("runbook_runs", map[string]any{"owner": "me"})["runs"].([]any); len(r) != 1 || r[0].(map[string]any)["owner"] != "grok-cli" {
		t.Fatalf("starter owns: %v", r)
	}
	me.mustFail("runbook_next", map[string]any{}, "owns no running run")
	me.mustFail("runbook_step_complete", map[string]any{"evidence": []any{map[string]any{"kind": "commit", "ref": "abcdef0"}}}, "owns no running run")
	me.mustFail("runbook_claim", map[string]any{}, "missing properties")

	out := me.must("runbook_claim", map[string]any{"run": id})
	if out["claimed"] != true || out["from"] != "grok-cli" || out["to"] != "claude-local" {
		t.Fatalf("claim: %v", out)
	}
	again := me.must("runbook_claim", map[string]any{"run": id[:12]}) // a cached prefix
	if again["claimed"] != false {
		t.Fatalf("claim again: %v", again)
	}
	next := me.must("runbook_next", map[string]any{})
	if next["runId"] != id || next["owner"] != "claude-local" || next["waitingOn"] != "agent" {
		t.Fatalf("next after claim: %v", next)
	}
	other.mustFail("runbook_next", map[string]any{}, "owns no running run")
	me.must("runbook_step_complete", map[string]any{"evidence": []any{map[string]any{"kind": "commit", "ref": "abcdef0"}}}) // default run: the owned one
	if r := me.must("runbook_runs", map[string]any{"owner": "me"})["runs"].([]any); len(r) != 1 || r[0].(map[string]any)["agent"] != "grok-cli" {
		t.Fatalf("owner=me: %v", r)
	}
	if r := me.must("runbook_runs", map[string]any{"owner": "grok-cli"})["runs"].([]any); len(r) != 0 {
		t.Fatalf("owner=grok-cli: %v", r)
	}
	h := me.must("runbook_history", map[string]any{"run": id})
	found := false
	for _, e := range h["events"].([]any) {
		ev := e.(map[string]any)
		if ev["type"] == "run.claimed" && ev["from"] == "grok-cli" && ev["to"] == "claude-local" {
			found = true
		}
	}
	if !found {
		t.Fatalf("history: %v", h)
	}

	// Offline: the cache knows the owner.
	srv.Close()
	off := me.must("runbook_runs", map[string]any{"owner": "me"})
	if r := off["runs"].([]any); off["stale"] != true || len(r) != 1 || r[0].(map[string]any)["owner"] != "claude-local" {
		t.Fatalf("offline owner=me: %v", off)
	}
	if r := other.must("runbook_runs", map[string]any{"owner": "me"})["runs"].([]any); len(r) != 0 {
		t.Fatalf("offline other owner=me: %v", r)
	}
	if !strings.HasPrefix(id, "run-") {
		t.Fatal(id)
	}
}
