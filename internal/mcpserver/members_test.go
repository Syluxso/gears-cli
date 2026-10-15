package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

type caller struct {
	t   *testing.T
	ctx context.Context
	cs  *mcp.ClientSession
}

func connect(t *testing.T, c verbs.Ctx) *caller {
	t.Helper()
	s := New(c, "test")
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &caller{t: t, ctx: ctx, cs: cs}
}

// call runs a tool; wantErr is the substring the error must carry ("" for
// success).
func (k *caller) call(name string, args map[string]any, wantErr string) map[string]any {
	k.t.Helper()
	r, err := k.cs.CallTool(k.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		k.t.Fatalf("%s: %v", name, err)
	}
	text := ""
	if len(r.Content) > 0 {
		text = r.Content[0].(*mcp.TextContent).Text
	}
	if r.IsError != (wantErr != "") || (wantErr != "" && !strings.Contains(text, wantErr)) {
		k.t.Fatalf("%s %v: isError %v, want %q: %s", name, args, r.IsError, wantErr, text)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(text), &out)
	return out
}

// GRS-42 on the local MCP: members_list, member_invite, invite_list,
// invite_accept and invite_decline go to gears-api (the fake) and pass its
// problems through in its own words.
func TestMembersAndInvitesTools(t *testing.T) {
	f := fakeapi.New("WS")
	f.Me = "u-me"
	f.Accounts = map[string]string{"bob@example.test": "u-bob", "me@example.test": "u-me", "ann@example.test": "u-ann"}
	f.Members = []api.Member{
		{UserID: "u-me", Email: "me@example.test", Name: "Me", Role: "owner", Status: "active"},
		{UserID: "u-ann", Email: "ann@example.test", Name: "Ann", Role: "member", Status: "active"},
		{UserID: "u-old", Email: "old@example.test", Name: "Old", Role: "member", Status: "removed"},
	}
	f.Invites = []api.Invite{
		{ID: "inv-mine-1", WorkspaceID: "w-acme", WorkspaceName: "Acme", InviteeID: "u-me", Role: "member", Status: "pending", CreatedAt: time.Now()},
		{ID: "inv-mine-2", WorkspaceID: "w-ops", WorkspaceName: "Ops", InviteeID: "u-me", Role: "owner", Status: "pending", CreatedAt: time.Now()},
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	k := connect(t, verbs.Ctx{Project: p, Agent: "tester"})

	tools, err := k.cs.ListTools(k.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, n := range []string{"members_list", "member_invite", "invite_list", "invite_accept", "invite_decline"} {
		if !have[n] {
			t.Fatalf("missing tool %s", n)
		}
	}

	ml := k.call("members_list", nil, "")
	if ml["workspace"] != p.WorkspaceID() || len(ml["members"].([]any)) != 2 || len(ml["invites"].([]any)) != 0 {
		t.Fatalf("members_list: %v", ml)
	}
	if all := k.call("members_list", map[string]any{"all": true}, ""); len(all["members"].([]any)) != 3 {
		t.Fatalf("members_list all: %v", all)
	}

	iv := k.call("member_invite", map[string]any{"email": "bob@example.test", "role": "owner"}, "")
	if iv["inviteeId"] != "u-bob" || iv["role"] != "owner" || iv["status"] != "pending" {
		t.Fatalf("member_invite: %v", iv)
	}
	if ml = k.call("members_list", nil, ""); len(ml["invites"].([]any)) != 1 {
		t.Fatalf("members_list after invite: %v", ml)
	}
	k.call("member_invite", map[string]any{"email": "nobody@example.test"}, "they must sign up first")
	k.call("member_invite", map[string]any{"email": "ann@example.test"}, "already a member")
	k.call("member_invite", map[string]any{"email": "bob@example.test"}, "already has a pending invite")
	k.call("member_invite", map[string]any{"email": "x@example.test", "role": "boss"}, "use member or owner")
	k.call("member_invite", map[string]any{}, "email (or userId) is required")

	// Revoke is a CLI verb (gears invite revoke); through the verb directly.
	c := verbs.Ctx{Project: p, Agent: "tester"}
	if out, err := verbs.InviteRevoke(c, verbs.InviteRevokeIn{ID: iv["id"].(string)}); err != nil || out.Status != "revoked" {
		t.Fatalf("revoke: %v %v", out, err)
	}
	if _, err := verbs.InviteRevoke(c, verbs.InviteRevokeIn{ID: iv["id"].(string)}); err == nil || !strings.Contains(err.Error(), "not a pending invite") {
		t.Fatalf("revoke twice: %v", err)
	}

	mine := k.call("invite_list", nil, "")
	if got := mine["invites"].([]any); len(got) != 2 || got[0].(map[string]any)["workspaceName"] != "Acme" {
		t.Fatalf("invite_list: %v", mine)
	}
	acc := k.call("invite_accept", map[string]any{"id": "inv-mine-1"}, "")
	if acc["invite"].(map[string]any)["status"] != "accepted" || !strings.Contains(acc["note"].(string), "Acme") {
		t.Fatalf("invite_accept: %v", acc)
	}
	dec := k.call("invite_decline", map[string]any{"id": "inv-mine-2"}, "")
	if dec["invite"].(map[string]any)["status"] != "declined" {
		t.Fatalf("invite_decline: %v", dec)
	}
	k.call("invite_accept", map[string]any{"id": "inv-mine-2"}, "not one of your pending invites")
	k.call("invite_accept", map[string]any{"id": ""}, "id is required")
	if mine = k.call("invite_list", nil, ""); len(mine["invites"].([]any)) != 0 {
		t.Fatalf("invite_list after: %v", mine)
	}

	// Not an owner: inviting an owner is the API's 403, said plainly.
	f.MeRole = "member"
	k.call("member_invite", map[string]any{"email": "bob@example.test", "role": "owner"}, "only a workspace owner may do that")

	// An API without the routes (before GRS-34).
	f.MembersStatus = http.StatusNotFound
	k.call("members_list", nil, "does not serve members and invites yet")
	k.call("invite_list", nil, "does not serve members and invites yet")
}

// Offline and logged out: a clear error, quickly, never a hang.
func TestMembersToolsOffline(t *testing.T) {
	// A listener that accepts and never answers: the short timeout ends it.
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer hang.Close()
	fakeapi.Login(t, hang.URL)
	old := verbs.MembersTimeout
	verbs.MembersTimeout = 300 * time.Millisecond
	defer func() { verbs.MembersTimeout = old }()
	p := fakeapi.Workspace(t)
	k := connect(t, verbs.Ctx{Project: p, Agent: "tester"})
	start := time.Now()
	k.call("members_list", nil, "gears-api did not answer (offline?)")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("members_list took %v", d)
	}

	// Nothing listening at all.
	fakeapi.Login(t, "http://127.0.0.1:1")
	k.call("invite_list", nil, "gears-api did not answer (offline?)")

	// Logged out.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	k.call("invite_list", nil, "not logged in; run gears login")
}
