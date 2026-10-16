package api

import (
	"context"
	"net/url"
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
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspaceId"`
	Runbook     string     `json:"runbook"`
	RunbookRef  string     `json:"runbookRef,omitempty"`
	RunbookVer  int        `json:"runbookVersion,omitempty"`
	Internal    bool       `json:"internal,omitempty"`
	StartedVia  string     `json:"startedVia,omitempty"`
	Status      string     `json:"status"`
	CurrentStep string     `json:"currentStep"`
	CardKey     string     `json:"cardKey"`
	AgentLabel  string     `json:"agentLabel"`
	StartedBy   string     `json:"startedBy"`
	Version     int        `json:"version"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	Steps       []RunStep  `json:"steps"`
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
