package api

import (
	"context"
	"net/url"
	"time"
)

// Official and adopted runbooks (GRS-59). Official runbooks are
// byz-orchestrator's public registry, each frozen or editable; a workspace
// adopts one into gears-api (a frozen copy is read-only and runs the
// official steps; an editable copy is the workspace's own).

// OfficialRunbook is one entry of the catalog, with the slugs this
// workspace adopted it as.
type OfficialRunbook struct {
	Ref         string    `json:"ref"`
	Version     int       `json:"version"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Frozen      bool      `json:"frozen"`
	Steps       []StepDef `json:"steps"`
	Adopted     []string  `json:"adopted"`
}

// AdoptedRunbook is a workspace's copy.
type AdoptedRunbook struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspaceId"`
	Slug           string    `json:"slug"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	SourceRef      string    `json:"sourceRef"`
	SourceVersion  int       `json:"sourceVersion"`
	Frozen         bool      `json:"frozen"`
	Steps          []StepDef `json:"steps"`
	CreatedByLabel string    `json:"createdByLabel,omitempty"`
	UpdatedByLabel string    `json:"updatedByLabel,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// RunbookEditIn changes an editable copy; nil fields are kept.
type RunbookEditIn struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Steps       []StepDef `json:"steps,omitempty"`
}

func runbooksPath(ws string) string { return "/api/v1/workspaces/" + url.PathEscape(ws) + "/runbooks" }

// OfficialRunbooks is the catalog (501 when gears-api has no orchestrator).
func (c *Client) OfficialRunbooks(ctx context.Context, ws string) ([]OfficialRunbook, error) {
	var out struct {
		Items []OfficialRunbook `json:"items"`
	}
	_, err := c.Do(ctx, "GET", runbooksPath(ws)+"/official", nil, &out)
	return out.Items, err
}

// AdoptedRunbooks lists the workspace's copies.
func (c *Client) AdoptedRunbooks(ctx context.Context, ws string) ([]AdoptedRunbook, error) {
	var out struct {
		Items []AdoptedRunbook `json:"items"`
	}
	_, err := c.Do(ctx, "GET", runbooksPath(ws), nil, &out)
	return out.Items, err
}

// AdoptRunbook copies the official runbook ref into the workspace.
func (c *Client) AdoptRunbook(ctx context.Context, ws, ref, as string) (*AdoptedRunbook, error) {
	var out AdoptedRunbook
	_, err := c.Do(ctx, "POST", runbooksPath(ws)+"/adopt", map[string]string{"ref": ref, "as": as}, &out)
	return &out, err
}

// EditRunbook changes an editable copy (409 gears.runbook_frozen otherwise).
func (c *Client) EditRunbook(ctx context.Context, ws, slug string, in RunbookEditIn) (*AdoptedRunbook, error) {
	var out AdoptedRunbook
	_, err := c.Do(ctx, "PUT", runbooksPath(ws)+"/"+url.PathEscape(slug), in, &out)
	return &out, err
}

// DeleteRunbook un-adopts a copy.
func (c *Client) DeleteRunbook(ctx context.Context, ws, slug string) error {
	_, err := c.Do(ctx, "DELETE", runbooksPath(ws)+"/"+url.PathEscape(slug), nil, nil)
	return err
}
