package sync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/runs"
	"github.com/Syluxso/gears-cli/internal/store"
)

// GRS-58: a pulled runs change fills the record cache, steps included; an
// older version never overwrites a newer one; a delete is ignored.
func TestApplyPulledRuns(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	mk := func(version int, status, current string) api.Change {
		r := api.Run{ID: "run-0199e0a1-0000-7000-8000-000000000001", WorkspaceID: "ws", Runbook: "gears-migrate", Status: status,
			CurrentStep: current, CardKey: "WS-1", AgentLabel: "claude-local", Version: version, StartedAt: now, UpdatedAt: now,
			Steps: []api.RunStep{{StepID: "a", Position: 1, Kind: "do", Title: "a", Status: "completed",
				Evidence: []api.Evidence{{Kind: "commit", Ref: "abc1234"}}}, {StepID: "b", Position: 2, Kind: "note", Title: "b", Status: "current"}},
			Events: []api.RunEvent{{RunID: "run-0199e0a1-0000-7000-8000-000000000001", Version: 1, Type: "run.started", At: now, Label: "claude-local"}}}
		if version == 2 { // GRS-72: the timeline rides in the row
			r.Events = append(r.Events, api.RunEvent{RunID: r.ID, Version: 2, Type: "step.completed", StepID: "a", At: now, Evidence: []api.Evidence{{Kind: "commit", Ref: "abc1234"}}})
		} else {
			r.Events = append(r.Events, api.RunEvent{RunID: r.ID, Version: 1, Type: "run.note", At: now})
		}
		b, _ := json.Marshal(r)
		return api.Change{Table: "runs", ID: "0199e0a1-0000-7000-8000-000000000001", Op: "upsert", Row: b}
	}
	r := &Report{}
	if ok, err := apply(context.Background(), nil, p, db, mk(2, "running", "b"), r); err != nil || !ok {
		t.Fatalf("apply v2: %v %v", ok, err)
	}
	if ok, _ := apply(context.Background(), nil, p, db, mk(1, "running", "a"), r); ok {
		t.Fatal("older version applied")
	}
	if ok, _ := apply(context.Background(), nil, p, db, api.Change{Table: "runs", ID: "x", Deleted: true}, r); ok {
		t.Fatal("delete applied")
	}
	got, err := runs.Get(p, "run-0199e0a1")
	if err != nil || got.Version != 2 || got.CurrentStep != "b" || len(got.Steps) != 2 || got.Steps[0].Evidence[0].Ref != "abc1234" {
		t.Fatalf("cached: %+v %v", got, err)
	}
	// GRS-72: events are kept from every row, even an older one (append-only).
	evs, err := runs.Events(p, got.ID)
	if err != nil || len(evs) != 3 || evs[0].Type != "run.started" || evs[2].Type != "step.completed" || evs[2].Evidence[0].Ref != "abc1234" {
		t.Fatalf("events: %+v %v", evs, err)
	}
	list, _ := runs.List(p, runs.Filter{Status: "active"})
	if len(list) != 1 {
		t.Fatalf("active: %v", list)
	}
}

// GRS-59: pulled runbooks (adopted copies) fill the cache; an older copy
// never overwrites a newer one; a delete drops it.
func TestApplyPulledRunbooks(t *testing.T) {
	res, err := project.Init(t.TempDir(), "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	id := "0199e0a1-0000-7000-8000-0000000000aa"
	mk := func(at time.Time, title string) api.Change {
		b, _ := json.Marshal(api.AdoptedRunbook{ID: id, WorkspaceID: "ws", Slug: "single-ticket-development", Title: title,
			SourceRef: "single-ticket-development", SourceVersion: 1, Frozen: true, Steps: []api.StepDef{{ID: "take", Kind: "card.move"}}, UpdatedAt: at})
		return api.Change{Table: "runbooks", ID: id, Op: "upsert", Row: b}
	}
	r := &Report{}
	if ok, err := apply(context.Background(), nil, p, db, mk(now, "new"), r); err != nil || !ok {
		t.Fatalf("apply: %v %v", ok, err)
	}
	if ok, _ := apply(context.Background(), nil, p, db, mk(now.Add(-time.Hour), "old"), r); ok {
		t.Fatal("older copy applied")
	}
	rb, err := runs.Runbook(p, "single-ticket-development")
	if err != nil || rb == nil || rb.Title != "new" || !rb.Frozen {
		t.Fatalf("cached: %+v %v", rb, err)
	}
	if err := runs.RefuseAdopted(p, "single-ticket-development"); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("guard: %v", err)
	}
	if ok, err := apply(context.Background(), nil, p, db, api.Change{Table: "runbooks", ID: id, Deleted: true}, r); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if rb, _ := runs.Runbook(p, "single-ticket-development"); rb != nil {
		t.Fatalf("still cached: %+v", rb)
	}
}
