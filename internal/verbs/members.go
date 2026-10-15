package verbs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
)

// Members and invites (GRS-42): who is on the workspace, inviting an
// existing Gears account by email, and the invites sent to me. These live
// on gears-api only (no local copy): each call goes to the API with a
// short timeout, and offline answers a plain error saying so.

// MembersTimeout bounds each membership call.
var MembersTimeout = 5 * time.Second

// membersClient is the logged-in API client, or an error that says what to
// do. A var so tests can stand in for it.
var membersClient = func() (*api.Client, error) {
	cl, err := api.Open(api.Host())
	if err != nil {
		if errors.Is(err, api.ErrNotLoggedIn) {
			return nil, errors.New("members and invites live on gears-api: not logged in; run gears login")
		}
		return nil, fmt.Errorf("members and invites live on gears-api: %w", err)
	}
	return cl, nil
}

// errMembersNotServed is an API without the membership routes.
var errMembersNotServed = errors.New("gears-api does not serve members and invites yet (404); it needs a rebuild")

// withAPI runs fn with a logged-in client under MembersTimeout, and words
// an unreachable API, or one without the routes, plainly. A problem the API
// sent (not an existing account, already a member, not an owner) passes
// through as it was said.
func withAPI[T any](fn func(ctx context.Context, cl *api.Client) (T, error)) (T, error) {
	var zero T
	cl, err := membersClient()
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), MembersTimeout)
	defer cancel()
	out, err := fn(ctx, cl)
	if err == nil {
		return out, nil
	}
	var p *api.Problem
	switch {
	case errors.As(err, &p):
		return zero, err
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "unreachable"):
		return zero, fmt.Errorf("gears-api did not answer (offline?): %w", err)
	case strings.Contains(err.Error(), ": 404 "), strings.Contains(err.Error(), ": 405 "):
		return zero, errMembersNotServed
	}
	return zero, err
}

func (c Ctx) workspaceForAPI() (string, error) {
	ws := c.Project.WorkspaceID()
	if ws == "" {
		return "", errors.New("this folder is not in a workspace; run gears init at the workspace root")
	}
	return ws, nil
}

type MembersListIn struct {
	All bool `json:"all,omitempty" jsonschema:"Include removed members."`
}

type MembersListOut struct {
	Workspace string       `json:"workspace"`
	Members   []api.Member `json:"members"`
	Invites   []api.Invite `json:"invites"`
}

// MembersList is the workspace's members (owners first) and its pending
// invites, from gears-api.
func MembersList(c Ctx, in MembersListIn) (*MembersListOut, error) {
	ws, err := c.workspaceForAPI()
	if err != nil {
		return nil, err
	}
	return withAPI(func(ctx context.Context, cl *api.Client) (*MembersListOut, error) {
		ms, err := cl.Members(ctx, ws, in.All)
		if err != nil {
			return nil, err
		}
		ivs, err := cl.WorkspaceInvites(ctx, ws)
		if err != nil {
			return nil, err
		}
		return &MembersListOut{Workspace: ws, Members: nonNil(ms), Invites: nonNil(ivs)}, nil
	})
}

type MemberInviteIn struct {
	Email  string `json:"email,omitempty" jsonschema:"The invitee's email. They must already have a Gears account."`
	UserID string `json:"userId,omitempty" jsonschema:"Or the invitee's account id, instead of email."`
	Role   string `json:"role,omitempty" jsonschema:"member (default) or owner. Only an owner may invite an owner."`
}

// MemberInvite invites an existing Gears account into this workspace.
func MemberInvite(c Ctx, in MemberInviteIn) (*api.Invite, error) {
	ws, err := c.workspaceForAPI()
	if err != nil {
		return nil, err
	}
	email, uid := strings.TrimSpace(in.Email), strings.TrimSpace(in.UserID)
	if email == "" && uid == "" {
		return nil, errors.New("email (or userId) is required")
	}
	role := strings.ToLower(strings.TrimSpace(in.Role))
	if role != "" && role != "member" && role != "owner" {
		return nil, fmt.Errorf("role %q: use member or owner", in.Role)
	}
	return withAPI(func(ctx context.Context, cl *api.Client) (*api.Invite, error) {
		return cl.CreateInvite(ctx, ws, email, uid, role)
	})
}

type InviteRevokeIn struct {
	ID string `json:"id" jsonschema:"The invite's id (members_list invites)."`
}

// InviteRevoke withdraws a pending invite of this workspace (its inviter
// or an owner).
func InviteRevoke(c Ctx, in InviteRevokeIn) (*api.Invite, error) {
	ws, err := c.workspaceForAPI()
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, errors.New("id is required")
	}
	return withAPI(func(ctx context.Context, cl *api.Client) (*api.Invite, error) {
		iv, err := cl.RevokeInvite(ctx, ws, id)
		if isGone(err) {
			return nil, fmt.Errorf("invite %s is not a pending invite of this workspace (members_list)", id)
		}
		return iv, err
	})
}

type InviteListIn struct{}

type InviteListOut struct {
	Invites []api.Invite `json:"invites"`
}

// InviteList is the invites sent to me (the logged-in account): pending
// ones, newest first, for any workspace.
func InviteList(c Ctx, _ InviteListIn) (*InviteListOut, error) {
	return withAPI(func(ctx context.Context, cl *api.Client) (*InviteListOut, error) {
		ivs, err := cl.MyInvites(ctx)
		if err != nil {
			return nil, err
		}
		return &InviteListOut{Invites: nonNil(ivs)}, nil
	})
}

type InviteRespondIn struct {
	ID string `json:"id" jsonschema:"The invite's id (from invite_list)."`
}

type InviteRespondOut struct {
	Invite api.Invite `json:"invite"`
	Note   string     `json:"note,omitempty"`
}

// InviteAccept accepts one of my invites: I become a member of its workspace.
func InviteAccept(c Ctx, in InviteRespondIn) (*InviteRespondOut, error) {
	return respondInvite(in, true)
}

// InviteDecline declines one of my invites.
func InviteDecline(c Ctx, in InviteRespondIn) (*InviteRespondOut, error) {
	return respondInvite(in, false)
}

func respondInvite(in InviteRespondIn, accept bool) (*InviteRespondOut, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, errors.New("id is required (invite_list)")
	}
	return withAPI(func(ctx context.Context, cl *api.Client) (*InviteRespondOut, error) {
		iv, err := cl.RespondInvite(ctx, id, accept)
		if isGone(err) {
			return nil, fmt.Errorf("invite %s is not one of your pending invites (invite_list)", id)
		}
		if err != nil {
			return nil, err
		}
		out := &InviteRespondOut{Invite: *iv}
		if accept {
			name := iv.WorkspaceName
			if name == "" {
				name = iv.WorkspaceSlug
			}
			out.Note = fmt.Sprintf("you are now a %s of %s; it is in your workspace list", iv.Role, name)
		}
		return out, nil
	})
}

// isGone is the API's plain "not found" problem: the row is not there for
// this caller (as opposed to a route the API lacks).
func isGone(err error) bool {
	var p *api.Problem
	return errors.As(err, &p) && p.Status == 404 && (p.Code == "gears.not_found" || p.Detail == "not found")
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
