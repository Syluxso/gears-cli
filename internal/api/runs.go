package api

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// Runbook runs (GRS-56, GRS-58). gears-api is the only door: it checks
// evidence and calls byz-orchestrator; the CLI never talks to the
// orchestrator or Temporal.

// Evidence is one proof item: {kind, ref, detail?}.
type Evidence struct {
	Kind   string         `json:"kind"`
	Ref    string         `json:"ref"`
	Detail map[string]any `json:"detail,omitempty"`
}

// Run is a run as gears-api records it.
type Run struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Runbook     string `json:"runbook"`
	RunbookRef  string `json:"runbookRef,omitempty"`
	RunbookVer  int    `json:"runbookVersion,omitempty"`
	Internal    bool   `json:"internal,omitempty"`
	StartedVia  string `json:"startedVia,omitempty"`
	Status      string `json:"status"`
	CurrentStep string `json:"currentStep"`
	CardKey     string `json:"cardKey"`
	AgentLabel  string `json:"agentLabel"`
	// OwnerLabel is the label kept working on the run (GRS-91): the
	// starter until a claim moves it.
	OwnerLabel string     `json:"ownerLabel,omitempty"`
	StartedBy  string     `json:"startedBy"`
	Version    int        `json:"version"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	// GRS-69: set on a canceled run (stopped on purpose).
	CancelReason  string     `json:"cancelReason,omitempty"`
	CanceledBy    string     `json:"canceledBy,omitempty"`
	CanceledLabel string     `json:"canceledLabel,omitempty"`
	CanceledVia   string     `json:"canceledVia,omitempty"`
	CanceledAt    *time.Time `json:"canceledAt,omitempty"`
	Steps         []RunStep  `json:"steps"`
	// Events is the run's timeline (GRS-72); sync pull's run rows carry it,
	// the REST run reads do not.
	Events []RunEvent `json:"events,omitempty"`
	// Stale: a refresh was asked for and could not happen; Note says why
	// (GRS-71). Not cached.
	Stale bool   `json:"stale,omitempty"`
	Note  string `json:"note,omitempty"`
}

type RunStep struct {
	RunID      string     `json:"runId"`
	StepID     string     `json:"stepId"`
	Position   int        `json:"position"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	Evidence   []Evidence `json:"evidence"`
	Note       string     `json:"note,omitempty"`
	SkipReason string     `json:"skipReason,omitempty"`
	DoneBy     string     `json:"doneBy,omitempty"`
	DoneLabel  string     `json:"doneLabel,omitempty"`
	DoneVia    string     `json:"doneVia,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// GRS-74: the step's params; GRS-84: who does it (system | agent) and
	// whether the system did; GRS-88: a person's reject or change request.
	Params       map[string]any `json:"params,omitempty"`
	Executor     string         `json:"executor,omitempty"`
	DoneBySystem bool           `json:"doneBySystem,omitempty"`
	SystemError  string         `json:"systemError,omitempty"`
	// Output (GRS-99): what a card.query step found.
	Output   map[string]any `json:"output,omitempty"`
	Decision *RunDecision   `json:"decision,omitempty"`
}

// RunDecision is a person's decision on an approval or gate step (GRS-88).
type RunDecision struct {
	Decision string    `json:"decision"` // approve | reject | changes
	Reason   string    `json:"reason,omitempty"`
	Actor    string    `json:"actor,omitempty"`
	Label    string    `json:"label,omitempty"`
	Via      string    `json:"via,omitempty"`
	Version  int       `json:"version"`
	At       time.Time `json:"at"`
}

// StepDef is one step of an inline runbook definition.
type StepDef struct {
	ID     string         `json:"id"`
	Kind   string         `json:"kind,omitempty"`
	Title  string         `json:"title,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

type RunStartIn struct {
	Runbook        string    `json:"runbook"`
	Steps          []StepDef `json:"steps,omitempty"`
	CardKey        string    `json:"cardKey,omitempty"`
	IdempotencyKey string    `json:"idempotencyKey,omitempty"`
	Agent          string    `json:"agent,omitempty"`
}

type RunStartOut struct {
	Created bool `json:"created"`
	Run     *Run `json:"run"`
}

type RunStepIn struct {
	Evidence []Evidence `json:"evidence,omitempty"`
	Note     string     `json:"note,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Agent    string     `json:"agent,omitempty"`
}

type RunStepOut struct {
	RunID       string   `json:"runId"`
	StepID      string   `json:"stepId"`
	StepStatus  string   `json:"stepStatus"`
	RunStatus   string   `json:"runStatus"`
	CurrentStep string   `json:"currentStep,omitempty"`
	Version     int      `json:"version"`
	Notes       []string `json:"notes,omitempty"`
	Run         *Run     `json:"run,omitempty"`
}

// StartRun starts a run in the workspace.
func (c *Client) StartRun(ctx context.Context, ws string, in RunStartIn) (*RunStartOut, error) {
	var out RunStartOut
	_, err := c.Do(ctx, "POST", "/api/v1/workspaces/"+url.PathEscape(ws)+"/runs", in, &out)
	return &out, err
}

// StepRun completes (complete true) or skips a step; step "" is the
// current one.
func (c *Client) StepRun(ctx context.Context, runID, step string, complete bool, in RunStepIn) (*RunStepOut, error) {
	if step == "" {
		step = "-"
	}
	verb := "skip"
	if complete {
		verb = "complete"
	}
	var out RunStepOut
	_, err := c.Do(ctx, "POST", "/api/v1/runs/"+url.PathEscape(runID)+"/steps/"+url.PathEscape(step)+"/"+verb, in, &out)
	return &out, err
}

// RunEvent is one transition of a run's timeline (GRS-72): run.started,
// step.completed, step.skipped, run.completed, run.canceled, ...
type RunEvent struct {
	RunID    string     `json:"runId"`
	Version  int        `json:"version"`
	Type     string     `json:"type"`
	StepID   string     `json:"stepId,omitempty"`
	At       time.Time  `json:"at"`
	Actor    string     `json:"actor,omitempty"`
	Label    string     `json:"label,omitempty"`
	Via      string     `json:"via,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Note     string     `json:"note,omitempty"`
	// GRS-84: a step event's executor; doneBySystem when the system did it.
	Executor     string `json:"executor,omitempty"`
	DoneBySystem bool   `json:"doneBySystem,omitempty"`
	// GRS-91: a run.claimed event's owner before and after.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// RunHistory is GET /runs/:id/events.
type RunHistory struct {
	RunID   string     `json:"runId"`
	Runbook string     `json:"runbook"`
	Status  string     `json:"status"`
	Version int        `json:"version"`
	Events  []RunEvent `json:"events"`
}

// RunEvents reads a run's timeline from the gears record (no orchestrator
// needed).
func (c *Client) RunEvents(ctx context.Context, runID string) (*RunHistory, error) {
	var out RunHistory
	_, err := c.Do(ctx, "GET", "/api/v1/runs/"+url.PathEscape(runID)+"/events", nil, &out)
	return &out, err
}

// CancelIn stops a run (GRS-69): reason required.
type CancelIn struct {
	Reason string `json:"reason"`
	Agent  string `json:"agent,omitempty"`
}

// CancelRun stops a running run with a reason; the answer is the canceled
// run from the gears record.
func (c *Client) CancelRun(ctx context.Context, runID string, in CancelIn) (*Run, error) {
	var out Run
	_, err := c.Do(ctx, "POST", "/api/v1/runs/"+url.PathEscape(runID)+"/cancel", in, &out)
	return &out, err
}

// GetRun reads one run; refresh asks gears-api to read the live state first.
func (c *Client) GetRun(ctx context.Context, runID string, refresh bool) (*Run, error) {
	q := ""
	if refresh {
		q = "?refresh=true"
	}
	var out Run
	_, err := c.Do(ctx, "GET", "/api/v1/runs/"+url.PathEscape(runID)+q, nil, &out)
	return &out, err
}

// ListRuns lists the workspace's runs, newest first.
func (c *Client) ListRuns(ctx context.Context, ws, status, card string) ([]Run, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if card != "" {
		q.Set("card", card)
	}
	path := "/api/v1/workspaces/" + url.PathEscape(ws) + "/runs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []Run
	_, err := c.Do(ctx, "GET", path, nil, &out)
	return out, err
}

// RunsFilter narrows ListRunsPage (GRS-71). Agent is the label that
// started the run, Owner the label that owns it (GRS-91).
type RunsFilter struct {
	Status, Card, Runbook, Agent, Owner, Cursor string
	Limit                                       int
}

// ListRunsPage lists the workspace's runs from the gears record (it needs
// no orchestrator) and the next page's cursor (X-Next-Cursor).
func (c *Client) ListRunsPage(ctx context.Context, ws string, f RunsFilter) ([]Run, string, error) {
	q := url.Values{}
	for k, v := range map[string]string{"status": f.Status, "card": f.Card, "runbook": f.Runbook, "agent": f.Agent, "owner": f.Owner, "cursor": f.Cursor} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	path := "/api/v1/workspaces/" + url.PathEscape(ws) + "/runs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []Run
	_, hdr, err := c.DoHeader(ctx, "GET", path, nil, &out)
	next := ""
	if hdr != nil {
		next = hdr.Get("X-Next-Cursor")
	}
	return out, next, err
}

// NextAbility is the current step's kind explained (GRS-87).
type NextAbility struct {
	Title            string   `json:"title"`
	Description      string   `json:"description,omitempty"`
	Tool             string   `json:"tool,omitempty"`
	EvidenceKinds    []string `json:"evidenceKinds"`
	RequiresEvidence bool     `json:"requiresEvidence"`
}

// NextStep is the run's current step as runbook_next shows it.
type NextStep struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Params      map[string]any `json:"params,omitempty"`
	Executor    string         `json:"executor"`
	Position    int            `json:"position"`
	Ability     NextAbility    `json:"ability"`
	SystemError string         `json:"systemError,omitempty"`
	Decision    *RunDecision   `json:"decision,omitempty"`
}

// RunNext is GET /runs/:id/next: what the agent does now.
type RunNext struct {
	RunID        string    `json:"runId,omitempty"`
	Runbook      string    `json:"runbook,omitempty"`
	Status       string    `json:"status,omitempty"`
	Card         string    `json:"card,omitempty"`
	Owner        string    `json:"owner,omitempty"`   // GRS-91
	Version      int       `json:"version,omitempty"` // the run's version
	Step         *NextStep `json:"step,omitempty"`
	Instructions string    `json:"instructions"`
	WaitingOn    string    `json:"waitingOn,omitempty"` // agent | system | human
	Done         int       `json:"done"`
	Total        int       `json:"total"`
	Progress     string    `json:"progress,omitempty"`
	Next         []string  `json:"next,omitempty"`
	// Outputs (GRS-99): the done steps' outputs by step id (card.query finds).
	Outputs  map[string]any `json:"outputs,omitempty"`
	Finished bool           `json:"finished,omitempty"`
	Message  string         `json:"message,omitempty"`
}

// NextRun reads what to do now on a run (gears record; no orchestrator).
func (c *Client) NextRun(ctx context.Context, runID string) (*RunNext, error) {
	var out RunNext
	_, err := c.Do(ctx, "GET", "/api/v1/runs/"+url.PathEscape(runID)+"/next", nil, &out)
	return &out, err
}

// ClaimIn names the label that takes a run over (GRS-91).
type ClaimIn struct {
	Agent string `json:"agent,omitempty"`
}

// ClaimOut is POST /runs/:id/claim: claimed false when the label already
// owned it.
type ClaimOut struct {
	Claimed bool   `json:"claimed"`
	From    string `json:"from"`
	To      string `json:"to"`
	Run     *Run   `json:"run"`
}

// ClaimRun makes the label the run's owner; recorded as run.claimed.
func (c *Client) ClaimRun(ctx context.Context, runID string, in ClaimIn) (*ClaimOut, error) {
	var out ClaimOut
	_, err := c.Do(ctx, "POST", "/api/v1/runs/"+url.PathEscape(runID)+"/claim", in, &out)
	return &out, err
}
