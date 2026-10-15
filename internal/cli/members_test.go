package cli

import (
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
)

// capture runs the root command in dir and returns what it printed.
func capture(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	runErr := runIn(t, dir, args...)
	w.Close()
	os.Stdout = old
	return <-done, runErr
}

// GRS-42: gears members, gears invite (and revoke), gears invites (accept,
// decline) against the fake API.
func TestMembersCommands(t *testing.T) {
	f := fakeapi.New("WS")
	f.Me = "u-me"
	f.Accounts = map[string]string{"bob@example.test": "u-bob"}
	f.Members = []api.Member{{UserID: "u-me", Email: "me@example.test", Name: "Me", Role: "owner", Status: "active"}}
	f.Invites = []api.Invite{{ID: "inv-1", WorkspaceID: "w-acme", WorkspaceName: "Acme", InviteeID: "u-me", Role: "member", Status: "pending"},
		{ID: "inv-2", WorkspaceID: "w-ops", WorkspaceName: "Ops", InviteeID: "u-me", Role: "member", Status: "pending"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	t.Setenv("GEARS_AGENT", "tester")
	p := fakeapi.Workspace(t)
	dir := p.Root
	defer func() { inviteRole, membersAll, jsonOut = "member", false, false }()

	out, err := capture(t, dir, "members")
	if err != nil || !strings.Contains(out, "u-me  owner  active  Me <me@example.test>") {
		t.Fatalf("members: %v\n%s", err, out)
	}
	out, err = capture(t, dir, "invite", "bob@example.test", "--role", "owner")
	if err != nil || !strings.Contains(out, "invited bob@example.test as owner") {
		t.Fatalf("invite: %v\n%s", err, out)
	}
	out, _ = capture(t, dir, "members")
	if !strings.Contains(out, "pending invites:") || !strings.Contains(out, "bob@example.test") {
		t.Fatalf("members after invite:\n%s", out)
	}
	if _, err := capture(t, dir, "invite", "nobody@example.test", "--role", "member"); err == nil || !strings.Contains(err.Error(), "must sign up first") {
		t.Fatalf("invite nobody: %v", err)
	}
	id := f.Invites[len(f.Invites)-1].ID
	out, err = capture(t, dir, "invite", "revoke", id)
	if err != nil || !strings.Contains(out, "revoked invite "+id) {
		t.Fatalf("revoke: %v\n%s", err, out)
	}

	out, err = capture(t, dir, "invites")
	if err != nil || !strings.Contains(out, "inv-1  member Acme") || !strings.Contains(out, "Ops") {
		t.Fatalf("invites: %v\n%s", err, out)
	}
	out, err = capture(t, dir, "invites", "accept", "inv-1")
	if err != nil || !strings.Contains(out, "accepted invite inv-1 to Acme") || !strings.Contains(out, "you are now a member of Acme") {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	out, err = capture(t, dir, "invites", "decline", "inv-2")
	if err != nil || !strings.Contains(out, "declined invite inv-2 to Ops") {
		t.Fatalf("decline: %v\n%s", err, out)
	}
	out, _ = capture(t, dir, "invites")
	if !strings.Contains(out, "no pending invites") {
		t.Fatalf("invites after:\n%s", out)
	}
}
