package sync

import (
	"context"
	"encoding/json"
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
				Evidence: []api.Evidence{{Kind: "commit", Ref: "abc1234"}}}, {StepID: "b", Position: 2, Kind: "note", Title: "b", Status: "current"}}}
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
	list, _ := runs.List(p, runs.Filter{Status: "active"})
	if len(list) != 1 {
		t.Fatalf("active: %v", list)
	}
}
