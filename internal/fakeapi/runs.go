package fakeapi

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Syluxso/gears-cli/internal/api"
)

// Runs (GRS-56/58): gears-api's runbook surface on an in-memory model.
// A registered runbook (no steps) has steps a and b. Evidence whose ref is
// "missing" is refused as gears-api refuses evidence that does not exist.
// Every change lands in the pull log as table runs, id the uuid part.

var (
	reWsRuns  = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)/runs$`)
	reRun     = regexp.MustCompile(`^/api/v1/runs/([^/]+)$`)
	reRunStep = regexp.MustCompile(`^/api/v1/runs/([^/]+)/steps/([^/]+)/(complete|skip)$`)
)

// RunBodies are the request bodies of every run write, in order.
func (s *Server) RunBodies() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.runBodies...)
}

func (s *Server) saveRun(r *api.Run, by string) {
	b, _ := json.Marshal(r)
	s.put("runs", strings.TrimPrefix(r.ID, "run-"), b, "upsert", by)
}

func (s *Server) getRun(id string) *api.Run {
	raw := s.rows["runs"][strings.TrimPrefix(id, "run-")]
	if raw == nil {
		return nil
	}
	var r api.Run
	_ = json.Unmarshal(raw, &r)
	return &r
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if !reWsRuns.MatchString(path) && !reRun.MatchString(path) && !reRunStep.MatchString(path) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var body map[string]any
	if r.Method == http.MethodPost {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		s.runBodies = append(s.runBodies, body)
	}
	now := time.Now().UTC()
	ok := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case reWsRuns.MatchString(path) && r.Method == http.MethodPost:
		ws := reWsRuns.FindStringSubmatch(path)[1]
		var in api.RunStartIn
		b, _ := json.Marshal(body)
		_ = json.Unmarshal(b, &in)
		steps := in.Steps
		if len(steps) == 0 {
			steps = []api.StepDef{{ID: "a"}, {ID: "b"}}
		}
		run := &api.Run{ID: "run-" + uuid.NewString(), WorkspaceID: ws, Runbook: in.Runbook, Status: "running", CardKey: in.CardKey,
			AgentLabel: in.Agent, StartedBy: "u-tester", Version: 1, StartedAt: now, UpdatedAt: now}
		for i, d := range steps {
			kind := d.Kind
			if kind == "" {
				kind = "freeform"
			}
			st := api.RunStep{RunID: run.ID, StepID: d.ID, Position: i + 1, Kind: kind, Title: d.ID, Status: "pending", Evidence: []api.Evidence{}}
			if i == 0 {
				st.Status, st.StartedAt = "current", &now
				run.CurrentStep = d.ID
			}
			run.Steps = append(run.Steps, st)
		}
		s.saveRun(run, in.Agent)
		ok(http.StatusCreated, api.RunStartOut{Created: true, Run: run})
	case reWsRuns.MatchString(path):
		status := r.URL.Query().Get("status")
		out := []api.Run{}
		for id := range s.rows["runs"] {
			if run := s.getRun(id); run != nil && (status == "" || run.Status == status) {
				out = append(out, *run)
			}
		}
		ok(http.StatusOK, out)
	case reRun.MatchString(path):
		run := s.getRun(reRun.FindStringSubmatch(path)[1])
		if run == nil {
			problem(w, 404, "gears.not_found", "not found")
			return true
		}
		ok(http.StatusOK, run)
	default:
		m := reRunStep.FindStringSubmatch(path)
		run := s.getRun(m[1])
		if run == nil {
			problem(w, 404, "gears.not_found", "not found")
			return true
		}
		var in api.RunStepIn
		b, _ := json.Marshal(body)
		_ = json.Unmarshal(b, &in)
		step := m[2]
		if step == "-" {
			step = run.CurrentStep
		}
		if run.Status != "running" {
			problem(w, 409, "run.finished", "run is "+run.Status)
			return true
		}
		if step != run.CurrentStep {
			problem(w, 409, "step.not_current", "the current step is "+run.CurrentStep)
			return true
		}
		for _, e := range in.Evidence {
			if e.Ref == "missing" {
				problem(w, 422, "evidence.invalid", "evidence[0]: no "+e.Kind+" missing in workspace")
				return true
			}
		}
		if m[3] == "skip" && strings.TrimSpace(in.Reason) == "" {
			problem(w, 400, "gears.bad_request", "skip needs a reason")
			return true
		}
		idx := 0
		for i := range run.Steps {
			if run.Steps[i].StepID == step {
				idx = i
			}
		}
		st := &run.Steps[idx]
		st.FinishedAt, st.DoneLabel, st.Note = &now, in.Agent, in.Note
		if m[3] == "complete" {
			st.Status, st.Evidence = "completed", in.Evidence
		} else {
			st.Status, st.SkipReason = "skipped", in.Reason
		}
		run.Version++
		if idx+1 < len(run.Steps) {
			run.Steps[idx+1].Status, run.Steps[idx+1].StartedAt = "current", &now
			run.CurrentStep = run.Steps[idx+1].StepID
		} else {
			run.CurrentStep, run.Status, run.FinishedAt = "", "completed", &now
		}
		s.saveRun(run, in.Agent)
		ok(http.StatusOK, api.RunStepOut{RunID: run.ID, StepID: st.StepID, StepStatus: st.Status, RunStatus: run.Status,
			CurrentStep: run.CurrentStep, Version: run.Version, Run: run})
	}
	return true
}
