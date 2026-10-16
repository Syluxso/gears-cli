package verbs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/store"
)

// Workspace soft delete and restore (GRS-80). They live on gears-api:
// delete is for an owner and needs the slug typed back; restore is for an
// owner of the deleted workspace. The local cache of a deleted workspace
// keeps its data and refuses reads (store.Open) until a restore, done here
// or elsewhere and heard by gears sync, clears the mark.

// CheckLive refuses with the plain message when this binding's cache was
// marked deleted (store.DeletedError); nil otherwise.
func (c Ctx) CheckLive() error {
	if c.Project == nil {
		return nil
	}
	return store.CheckLive(c.Project)
}

type WorkspaceListIn struct {
	Deleted bool `json:"deleted,omitempty" jsonschema:"List your deleted workspaces (the ones you own) instead, with deletedAt and deletedBy."`
}

type WorkspaceListOut struct {
	Workspaces []api.Workspace        `json:"workspaces,omitempty"`
	Deleted    []api.DeletedWorkspace `json:"deleted,omitempty"`
	// Here is this binding's workspace id, when there is one.
	Here string `json:"here,omitempty"`
}

// WorkspaceList is the account's workspaces on gears-api, or with deleted
// the deleted ones the caller owns.
func WorkspaceList(c Ctx, in WorkspaceListIn) (*WorkspaceListOut, error) {
	return withAPI(func(ctx context.Context, cl *api.Client) (*WorkspaceListOut, error) {
		out := &WorkspaceListOut{}
		if c.Project != nil {
			out.Here = c.Project.WorkspaceID()
		}
		if in.Deleted {
			del, err := cl.ListDeletedWorkspaces(ctx)
			if err != nil {
				return nil, err
			}
			out.Deleted = nonNil(del)
			return out, nil
		}
		ws, err := cl.ListWorkspaces(ctx)
		if err != nil {
			return nil, err
		}
		out.Workspaces = nonNil(ws)
		return out, nil
	})
}

type WorkspaceDeleteIn struct {
	Workspace string `json:"workspace,omitempty" jsonschema:"Workspace id or slug; default this workspace."`
	Confirm   string `json:"confirm" jsonschema:"The workspace slug, typed back. Ask your person first: they must say to delete it."`
}

type WorkspaceDeleteOut struct {
	Deleted string `json:"deleted"`
	Slug    string `json:"slug"`
	Here    bool   `json:"here" jsonschema:"This binding's workspace: its local cache now refuses reads."`
	Note    string `json:"note"`
}

// WorkspaceDelete soft deletes a workspace on gears-api (owners only,
// confirm = slug). When it is this binding's, the cache is marked deleted.
func WorkspaceDelete(c Ctx, in WorkspaceDeleteIn) (*WorkspaceDeleteOut, error) {
	if strings.TrimSpace(in.Confirm) == "" {
		return nil, errors.New("confirm is required: the workspace slug, typed back")
	}
	return withAPI(func(ctx context.Context, cl *api.Client) (*WorkspaceDeleteOut, error) {
		cl.Label = c.agent() // deletedBy on gears-api
		w, err := resolveWorkspace(ctx, cl, c, in.Workspace)
		if err != nil {
			return nil, err
		}
		if err := cl.DeleteWorkspace(ctx, w.ID, strings.TrimSpace(in.Confirm)); err != nil {
			return nil, err
		}
		out := &WorkspaceDeleteOut{Deleted: w.ID, Slug: w.Slug,
			Note: "soft deleted on gears-api: nothing in it is reachable until an owner restores it (gears workspace restore " + w.Slug + ")"}
		if c.Project != nil && strings.EqualFold(c.Project.WorkspaceID(), w.ID) {
			out.Here = true
			_ = store.MarkDeleted(c.Project, w.Slug)
			out.Note += "; the local cache is kept and refuses reads until then"
		}
		return out, nil
	})
}

type WorkspaceRestoreIn struct {
	Workspace string `json:"workspace,omitempty" jsonschema:"Id or slug of a deleted workspace you own (workspace_list deleted=true); default this workspace."`
}

type WorkspaceRestoreOut struct {
	Workspace api.Workspace `json:"workspace"`
	Here      bool          `json:"here" jsonschema:"This binding's workspace: its local cache reads again."`
	Note      string        `json:"note,omitempty"`
}

// WorkspaceRestore restores a deleted workspace on gears-api and clears
// the local mark when it is this binding's.
func WorkspaceRestore(c Ctx, in WorkspaceRestoreIn) (*WorkspaceRestoreOut, error) {
	key := strings.TrimSpace(in.Workspace)
	if key == "" && c.Project != nil {
		key = c.Project.WorkspaceID()
	}
	if key == "" {
		return nil, errors.New("workspace is required (id or slug; gears workspace list --deleted)")
	}
	return withAPI(func(ctx context.Context, cl *api.Client) (*WorkspaceRestoreOut, error) {
		cl.Label = c.agent()
		w, err := cl.RestoreWorkspace(ctx, key)
		if err != nil {
			return nil, err
		}
		out := &WorkspaceRestoreOut{Workspace: *w}
		if c.Project != nil && strings.EqualFold(c.Project.WorkspaceID(), w.ID) {
			out.Here = true
			if _, err := store.ClearDeleted(c.Project); err != nil {
				return nil, err
			}
			out.Note = "restored; local reads work again. gears sync brings the cache up to date"
		}
		return out, nil
	})
}

// resolveWorkspace is the workspace a delete is about: key (id or slug)
// among the account's live workspaces, else this binding's.
func resolveWorkspace(ctx context.Context, cl *api.Client, c Ctx, key string) (*api.Workspace, error) {
	key = strings.TrimSpace(key)
	if key == "" && c.Project != nil {
		key = c.Project.WorkspaceID()
	}
	if key == "" {
		return nil, errors.New("workspace is required (id or slug); this folder is not in a workspace")
	}
	list, err := cl.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].ID, key) || strings.EqualFold(list[i].Slug, key) {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("no live workspace %q in your account (gears workspace list; already deleted ones: gears workspace list --deleted)", key)
}
