package api

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Workspace soft delete and restore (GRS-80 in gears-api).

// CodeWorkspaceDeleted is the problem code gears-api answers (410) for a
// deleted workspace: sync, upsert, and GET by its owner.
const CodeWorkspaceDeleted = "gears.workspace_deleted"

// IsWorkspaceDeleted reports a 410 gears.workspace_deleted answer.
func IsWorkspaceDeleted(err error) bool {
	var p *Problem
	if errors.As(err, &p) {
		return p.Code == CodeWorkspaceDeleted || (p.Status == http.StatusGone && p.Code == "")
	}
	var se *StreamError
	return errors.As(err, &se) && se.Status == http.StatusGone
}

// DeletedWorkspace is one of GET /workspaces?deleted=true.
type DeletedWorkspace struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name,omitempty"`
	DeletedAt time.Time `json:"deletedAt"`
	DeletedBy string    `json:"deletedBy,omitempty"`
}

// ListDeletedWorkspaces lists the caller's deleted workspaces (the ones it owns).
func (c *Client) ListDeletedWorkspaces(ctx context.Context) ([]DeletedWorkspace, error) {
	out := []DeletedWorkspace{}
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/workspaces?deleted=true", nil, &out)
	return out, err
}

// DeleteWorkspace soft deletes a workspace (owners only); confirm is its slug.
func (c *Client) DeleteWorkspace(ctx context.Context, id, confirm string) error {
	_, err := c.Do(ctx, http.MethodDelete, "/api/v1/workspaces/"+urlQuery(id)+"?confirm="+urlQuery(confirm), nil, nil)
	return err
}

// RestoreWorkspace restores a deleted workspace by id or slug (owners only).
func (c *Client) RestoreWorkspace(ctx context.Context, key string) (*Workspace, error) {
	var out Workspace
	_, err := c.Do(ctx, http.MethodPost, "/api/v1/workspaces/"+urlQuery(key)+"/restore", nil, &out)
	return &out, err
}
