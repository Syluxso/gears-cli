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
	Card           string        `json:"cardKey,omitempty" jsonschema:"Card key the run is for, e.g. GRS-55. Must be on the workspace's board."`
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

// RunbookStopIn stops a run (GRS-69); the same arguments as the remote
// MCP's runbook_stop.
type RunbookStopIn struct {
	Run    string `json:"run" jsonschema:"Run id (run-<uuid>, or a prefix of a cached one), from runbook_start or runbook_runs."`
	Reason string `json:"reason" jsonschema:"Why the run is stopped. Required (at most 2000 characters); it stays on the run."`
	AgentOpt
}

// RunbookStop cancels a running run through gears-api (byz-orchestrator
// cancels it: the current step and the run become canceled) and caches the
// answer.
func RunbookStop(c Ctx, in RunbookStopIn) (*api.Run, error) {
	if strings.TrimSpace(in.Run) == "" {
		return nil, errors.New("run required: its id (run-<uuid>) or a cached prefix")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, errors.New("stopping a run needs a reason")
	}
	cl, err := runsClient()
	if err != nil {
		return nil, err
	}
	agent := c.agentOr(in.Agent)
	id, err := c.pickRun(cl, in.Run, agent)
	if err != nil {
		return nil, err
	}
	ctx, cancel := runCtx()
	defer cancel()
	out, err := cl.CancelRun(ctx, id, api.CancelIn{Reason: strings.TrimSpace(in.Reason), Agent: agent})
	if err != nil {
		return nil, err
	}
	c.cacheRun(out)
	return out, nil
}

type RunbookStatusIn struct {
	Run     string `json:"run,omitempty" jsonschema:"Run id (or a cached prefix) for one run, from the gears record. Omit to list the workspace's runs."`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"With run: gears-api reads the live state from the orchestrator first; when it cannot, the record answers with stale: true and a note."`
	Status  string `json:"status,omitempty" jsonschema:"List filter: running (or active), completed, failed, canceled, terminated, timed_out."`
	Card    string `json:"card,omitempty" jsonschema:"List filter: card key."`
	AgentOpt
}

type RunbookStatusOut struct {
	Run   *api.Run  `json:"run,omitempty"`
	Runs  []api.Run `json:"runs,omitempty"`
	Stale bool      `json:"stale,omitempty"`
	Note  string    `json:"note,omitempty"`
}

// RunbookStatus is one run from the gears record (refreshed from the
// orchestrator only when asked) or the workspace's runs. It needs no
// orchestrator; with gears-api offline it answers the cache, stale.
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
			r, err := cl.GetRun(ctx, id, in.Refresh)
			if err == nil {
				c.cacheRun(r)
				return &RunbookStatusOut{Run: r, Stale: r.Stale, Note: r.Note}, nil
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

// ------------------------------------------------------------ runbook_runs

// RunbookRunsIn filters the workspace's runs (GRS-71); the same arguments
// as the remote MCP's runbook_runs.
type RunbookRunsIn struct {
	Status  string `json:"status,omitempty" jsonschema:"running (or active), completed, failed, canceled, terminated, timed_out."`
	Runbook string `json:"runbook,omitempty" jsonschema:"The runbook's name (an adopted slug, a registered ref or an inline name)."`
	Card    string `json:"card,omitempty" jsonschema:"Card key, e.g. GRS-55."`
	By      string `json:"by,omitempty" jsonschema:"Only runs started by this agent label."`
	Mine    bool   `json:"mine,omitempty" jsonschema:"Only runs started by the label you act as."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Page size, 1-200 (default 50)."`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next from the previous page."`
	AgentOpt
}

// RunsStep is a run's current step in a summary.
type RunsStep struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title,omitempty"`
	Position  int        `json:"position"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// RunsItem is one run as runbook_runs lists it (the remote shape).
type RunsItem struct {
	ID          string     `json:"id"`
	Runbook     string     `json:"runbook"`
	RunbookRef  string     `json:"runbookRef,omitempty"`
	Status      string     `json:"status"`
	CurrentStep *RunsStep  `json:"currentStep,omitempty"`
	Done        int        `json:"done"`
	Total       int        `json:"total"`
	Progress    string     `json:"progress"`
	Agent       string     `json:"agent"`
	Card        string     `json:"card,omitempty"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type RunbookRunsOut struct {
	Runs  []RunsItem `json:"runs"`
	Next  string     `json:"next,omitempty"`
	Stale bool       `json:"stale,omitempty"`
	Note  string     `json:"note,omitempty"`
}

func runsItem(r api.Run) RunsItem {
	out := RunsItem{ID: r.ID, Runbook: r.Runbook, RunbookRef: r.RunbookRef, Status: r.Status, Total: len(r.Steps), Agent: r.AgentLabel,
		Card: r.CardKey, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, UpdatedAt: r.UpdatedAt}
	for _, s := range r.Steps {
		if s.Status == "completed" || s.Status == "skipped" {
			out.Done++
		}
	}
	if cur := runs.Current(&r); cur != nil {
		out.CurrentStep = &RunsStep{ID: cur.StepID, Kind: cur.Kind, Title: cur.Title, Position: cur.Position, StartedAt: cur.StartedAt}
	}
	out.Progress = fmt.Sprintf("%d/%d", out.Done, out.Total)
	return out
}

// RunbookRuns lists runs from the gears record through gears-api (it needs
// no orchestrator), caching them; with gears-api offline, the local cache
// with stale: true.
func RunbookRuns(c Ctx, in RunbookRunsIn) (*RunbookRunsOut, error) {
	by := strings.TrimSpace(in.By)
	if in.Mine {
		by = c.agentOr(in.Agent)
	}
	f := api.RunsFilter{Status: in.Status, Card: in.Card, Runbook: in.Runbook, Agent: by, Limit: in.Limit, Cursor: in.Cursor}
	var apiErr error
	if ws, err := c.runsWorkspace(); err != nil {
		apiErr = err
	} else if cl, err := runsClient(); err != nil {
		apiErr = err
	} else {
		ctx, cancel := runCtx()
		defer cancel()
		list, next, err := cl.ListRunsPage(ctx, ws, f)
		if err == nil {
			out := &RunbookRunsOut{Runs: []RunsItem{}, Next: next}
			for i := range list {
				c.cacheRun(&list[i])
				out.Runs = append(out.Runs, runsItem(list[i]))
			}
			return out, nil
		}
		var p *api.Problem
		if errors.As(err, &p) && p.Status < 500 {
			return nil, err
		}
		apiErr = err
	}
	list, err := runs.List(c.Project, runs.Filter{Status: in.Status, Card: in.Card, Agent: by, Runbook: in.Runbook})
	if err != nil {
		return nil, err
	}
	out := &RunbookRunsOut{Runs: []RunsItem{}, Stale: true, Note: "gears-api did not answer (" + apiErr.Error() + "); this is the local cache"}
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	for i, r := range list {
		if i == limit {
			break
		}
		out.Runs = append(out.Runs, runsItem(r))
	}
	return out, nil
}
