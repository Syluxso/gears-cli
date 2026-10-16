package verbs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/runs"
)

// Runbook runs (GRS-56): start, complete, skip and status go to gears-api,
// which checks evidence and calls byz-orchestrator. Never the orchestrator
// or Temporal from here, and never the superseded local run engine. Every
// answer's run is written to the local cache (GRS-58) so status and
// offline reads see it at once.

// runsClient is gears-api for the runbook verbs. A var so tests can point
// it at a fake.
var runsClient = func() (*api.Client, error) {
	cl, err := api.Open(api.Host())
	if errors.Is(err, api.ErrNotLoggedIn) {
		return nil, errors.New("runbooks run on gears-api: run gears login first")
	}
	return cl, err
}

func runCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// AgentOpt is the per-call agent label the runbook tools take.
type AgentOpt struct {
	Agent string `json:"agent,omitempty" jsonschema:"Your agent label for this call. Default: the label this server runs as."`
}

func (c Ctx) agentOr(a string) string {
	if a = strings.TrimSpace(a); a != "" {
		return a
	}
	return c.agent()
}

func (c Ctx) cacheRun(r *api.Run) {
	if r == nil || c.Project == nil {
		return
	}
	_, _ = runs.Save(c.Project, *r)
}

func (c Ctx) runsWorkspace() (string, error) {
	ws := c.Project.WorkspaceID()
	if ws == "" {
		return "", errors.New("this workspace is not on gears-api yet: run gears login, then gears sync")
	}
	return ws, nil
}

type RunbookStartIn struct {
	Runbook        string        `json:"runbook" jsonschema:"A registered runbook's name (e.g. gears-migrate), or the name of the inline steps."`
	Steps          []api.StepDef `json:"steps,omitempty" jsonschema:"Inline definition, in order: each {id, kind?, title?, params?}; kinds come from the orchestrator's ability catalog (freeform, do, ask, approval, gate, note, card.move, card.comment, git.branch, git.commit, git.push, git.merge). Omit to start the registered runbook."`
	Card           string        `json:"card,omitempty" jsonschema:"Card key the run is for, e.g. GRS-55. Must be on the workspace's board."`
	IdempotencyKey string        `json:"idempotencyKey,omitempty" jsonschema:"Same key again returns the run it started."`
	AgentOpt
}

// RunbookStart starts a run through gears-api.
func RunbookStart(c Ctx, in RunbookStartIn) (*api.RunStartOut, error) {
	ws, err := c.runsWorkspace()
	if err != nil {
		return nil, err
	}
	cl, err := runsClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := runCtx()
	defer cancel()
	out, err := cl.StartRun(ctx, ws, api.RunStartIn{Runbook: in.Runbook, Steps: in.Steps, CardKey: in.Card, IdempotencyKey: in.IdempotencyKey, Agent: c.agentOr(in.Agent)})
	if err != nil {
		return nil, err
	}
	c.cacheRun(out.Run)
	return out, nil
}

// RunPick names a run and a step; both may be left out.
type RunPick struct {
	Run  string `json:"run,omitempty" jsonschema:"Run id (run-<uuid>, or a prefix of a cached one). Omit for your one running run."`
	Step string `json:"step,omitempty" jsonschema:"Step id. Omit for the run's current step."`
	Note string `json:"note,omitempty"`
	AgentOpt
}

// pickRun resolves the run: a full id as given, a cached prefix, else the
// one running run this agent started (asked of gears-api).
func (c Ctx) pickRun(cl *api.Client, key, agent string) (string, error) {
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, "run-") && len(key) == len("run-")+36 {
		return key, nil
	}
	if key != "" {
		r, err := runs.Get(c.Project, key)
		if err != nil {
			return "", err
		}
		return r.ID, nil
	}
	ws, err := c.runsWorkspace()
	if err != nil {
		return "", err
	}
	ctx, cancel := runCtx()
	defer cancel()
	list, err := cl.ListRuns(ctx, ws, "running", "")
	if err != nil {
		return "", err
	}
	var mine []string
	for _, r := range list {
		if r.AgentLabel == agent {
			mine = append(mine, r.ID)
		}
	}
	switch len(mine) {
	case 1:
		return mine[0], nil
	case 0:
		return "", fmt.Errorf("%s has no running run; pass run", agent)
	}
	return "", fmt.Errorf("%s has %d running runs (%s); pass run", agent, len(mine), strings.Join(mine, ", "))
}

type RunbookStepCompleteIn struct {
	RunPick
	Evidence []api.Evidence `json:"evidence,omitempty" jsonschema:"Proof, each {kind, ref, detail?}: session (entry id or its 8-digit short), artifact (file name prefix--slug.md or id), desk (item id; detail.state e.g. closed; an ask/approval step needs it answered), card (key; detail.state the state it must be in), commit (sha, 7-40 hex; accepted on shape only; detail.repo, detail.branch). The step's ability decides which kinds count. Each must exist in the workspace on gears-api (sync first) or the call is refused and the step stays current."`
}

type RunbookStepSkipIn struct {
	RunPick
	Reason string `json:"reason" jsonschema:"Why the step is skipped. Required; it stays on the run."`
}

func (c Ctx) stepRun(p RunPick, complete bool, body api.RunStepIn) (*api.RunStepOut, error) {
	cl, err := runsClient()
	if err != nil {
		return nil, err
	}
	agent := c.agentOr(p.Agent)
	id, err := c.pickRun(cl, p.Run, agent)
	if err != nil {
		return nil, err
	}
	body.Agent, body.Note = agent, p.Note
	ctx, cancel := runCtx()
	defer cancel()
	out, err := cl.StepRun(ctx, id, strings.TrimSpace(p.Step), complete, body)
	if err != nil {
		return nil, err
	}
	c.cacheRun(out.Run)
	return out, nil
}

// RunbookStepComplete completes a step with evidence through gears-api.
func RunbookStepComplete(c Ctx, in RunbookStepCompleteIn) (*api.RunStepOut, error) {
	return c.stepRun(in.RunPick, true, api.RunStepIn{Evidence: in.Evidence})
}

// RunbookStepSkip skips a step with a reason through gears-api.
func RunbookStepSkip(c Ctx, in RunbookStepSkipIn) (*api.RunStepOut, error) {
	if strings.TrimSpace(in.Reason) == "" {
		return nil, errors.New("skip needs a reason")
	}
	return c.stepRun(in.RunPick, false, api.RunStepIn{Reason: in.Reason})
}

type RunbookStatusIn struct {
	Run    string `json:"run,omitempty" jsonschema:"Run id (or a cached prefix) for one run. Omit to list the workspace's runs."`
	Status string `json:"status,omitempty" jsonschema:"List filter: running (or active), completed, failed, canceled, terminated, timed_out."`
	Card   string `json:"card,omitempty" jsonschema:"List filter: card key."`
	AgentOpt
}

type RunbookStatusOut struct {
	Run   *api.Run  `json:"run,omitempty"`
	Runs  []api.Run `json:"runs,omitempty"`
	Stale bool      `json:"stale,omitempty"`
	Note  string    `json:"note,omitempty"`
}

// RunbookStatus is one run (refreshed by gears-api from the orchestrator)
// or the workspace's runs. Offline it answers the cache with stale: true.
func RunbookStatus(c Ctx, in RunbookStatusIn) (*RunbookStatusOut, error) {
	cl, err := runsClient()
	var apiErr error
	if err == nil {
		ctx, cancel := runCtx()
		defer cancel()
		if key := strings.TrimSpace(in.Run); key != "" {
			id, err := c.pickRun(cl, key, c.agentOr(in.Agent))
			if err != nil {
				return nil, err
			}
			r, err := cl.GetRun(ctx, id, true)
			if err == nil {
				c.cacheRun(r)
				return &RunbookStatusOut{Run: r}, nil
			}
			apiErr = err
		} else if ws, err := c.runsWorkspace(); err != nil {
			apiErr = err
		} else {
			list, err := cl.ListRuns(ctx, ws, in.Status, in.Card)
			if err == nil {
				for i := range list {
					c.cacheRun(&list[i])
				}
				if list == nil {
					list = []api.Run{}
				}
				return &RunbookStatusOut{Runs: list}, nil
			}
			apiErr = err
		}
	} else {
		apiErr = err
	}
	var p *api.Problem
	if errors.As(apiErr, &p) && p.Status < 500 {
		return nil, apiErr
	}
	note := "gears-api did not answer (" + apiErr.Error() + "); this is the local cache"
	if key := strings.TrimSpace(in.Run); key != "" {
		r, err := runs.Get(c.Project, key)
		if err != nil {
			return nil, fmt.Errorf("%s; %w", note, err)
		}
		return &RunbookStatusOut{Run: r, Stale: true, Note: note}, nil
	}
	list, err := runs.List(c.Project, runs.Filter{Status: in.Status, Card: in.Card})
	if err != nil {
		return nil, err
	}
	return &RunbookStatusOut{Runs: list, Stale: true, Note: note}, nil
}

// recordSummary is a cached record run as status shows it.
func recordSummary(r api.Run) RunSummary {
	out := RunSummary{ID: r.ID, Runbook: r.Runbook, Card: r.CardKey, Agent: r.AgentLabel, Total: len(r.Steps)}
	for _, s := range r.Steps {
		if s.Status == "completed" || s.Status == "skipped" {
			out.Done++
		}
	}
	if cur := runs.Current(&r); cur != nil {
		out.Current = &RunStep{Idx: cur.Position, Kind: cur.Kind, Text: cur.Title, State: cur.Status, StartedAt: ts(deref(cur.StartedAt))}
	}
	return out
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
