package api

import (
	"context"
	"net/http"
	"time"
)

// Workspace membership and invites (GRS-34 in gears-api, GRS-42 here).
// Shapes copied from gears-api members.go (Member, Invite).

// Member is one person on a workspace. Role owner | member; status
// active | removed.
type Member struct {
	UserID    string    `json:"userId"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	InvitedBy string    `json:"invitedBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Invite is an invitation of an existing account into a workspace. Status
// pending | accepted | declined | revoked.
type Invite struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspaceId"`
	WorkspaceSlug string     `json:"workspaceSlug,omitempty"`
	WorkspaceName string     `json:"workspaceName,omitempty"`
	InviteeID     string     `json:"inviteeId"`
	InviteeEmail  string     `json:"inviteeEmail,omitempty"`
	Role          string     `json:"role"`
	Status        string     `json:"status"`
	InvitedBy     string     `json:"invitedBy,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	RespondedAt   *time.Time `json:"respondedAt,omitempty"`
}

// Members lists a workspace's members: active ones, or every one with all.
func (c *Client) Members(ctx context.Context, ws string, all bool) ([]Member, error) {
	path := "/api/v1/workspaces/" + urlQuery(ws) + "/members"
	if all {
		path += "?all=true"
	}
	out := []Member{}
	_, err := c.Do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// RemoveMember marks a member removed (owners only).
func (c *Client) RemoveMember(ctx context.Context, ws, userID string) (*Member, error) {
	var out Member
	_, err := c.Do(ctx, http.MethodDelete, "/api/v1/workspaces/"+urlQuery(ws)+"/members/"+urlQuery(userID), nil, &out)
	return &out, err
}

// WorkspaceInvites lists a workspace's pending invites.
func (c *Client) WorkspaceInvites(ctx context.Context, ws string) ([]Invite, error) {
	out := []Invite{}
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/workspaces/"+urlQuery(ws)+"/invites", nil, &out)
	return out, err
}

// CreateInvite invites an existing account by email (or user id) with a
// role (member when empty).
func (c *Client) CreateInvite(ctx context.Context, ws, email, userID, role string) (*Invite, error) {
	body := map[string]string{}
	if email != "" {
		body["email"] = email
	}
	if userID != "" {
		body["userId"] = userID
	}
	if role != "" {
		body["role"] = role
	}
	var out Invite
	_, err := c.Do(ctx, http.MethodPost, "/api/v1/workspaces/"+urlQuery(ws)+"/invites", body, &out)
	return &out, err
}

// RevokeInvite withdraws a pending invite (its inviter or an owner).
func (c *Client) RevokeInvite(ctx context.Context, ws, id string) (*Invite, error) {
	var out Invite
	_, err := c.Do(ctx, http.MethodDelete, "/api/v1/workspaces/"+urlQuery(ws)+"/invites/"+urlQuery(id), nil, &out)
	return &out, err
}

// MyInvites lists the caller's pending invites, newest first.
func (c *Client) MyInvites(ctx context.Context) ([]Invite, error) {
	out := []Invite{}
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/invites", nil, &out)
	return out, err
}

// RespondInvite accepts or declines the caller's own pending invite.
func (c *Client) RespondInvite(ctx context.Context, id string, accept bool) (*Invite, error) {
	verb := "decline"
	if accept {
		verb = "accept"
	}
	var out Invite
	_, err := c.Do(ctx, http.MethodPost, "/api/v1/invites/"+urlQuery(id)+"/"+verb, struct{}{}, &out)
	return &out, err
}
