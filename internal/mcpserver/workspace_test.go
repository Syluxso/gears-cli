package mcpserver

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-80 on the local MCP: workspace_list, workspace_delete and
// workspace_restore through gears-api (the fake), and every other tool
// refusing while this workspace is deleted.
func TestWorkspaceDeleteRestoreTools(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	t.Setenv("GEARS_CARD_SYNC", "off")
	p := fakeapi.Workspace(t)
	f.SetWorkspace(p.ID, p.Slug)
	if _, err := session.AppendFor(p, "", "done", "tester", "kept"); err != nil {
		t.Fatal(err)
	}
	k := connect(t, verbs.Ctx{Project: p, Agent: "tester"})

	if l := k.call("workspace_list", nil, ""); l["here"] != p.ID || len(l["workspaces"].([]any)) != 1 {
		t.Fatalf("workspace_list: %v", l)
	}
	k.call("workspace_delete", map[string]any{"confirm": " "}, "confirm is required")
	k.call("workspace_delete", map[string]any{"confirm": "nope"}, "does not match")
	f.MeRole = "member"
	k.call("workspace_delete", map[string]any{"confirm": p.Slug}, "only a workspace owner")
	f.MeRole = ""
	k.call("workspace_delete", map[string]any{"workspace": "no-such", "confirm": "no-such"}, "no live workspace")
	d := k.call("workspace_delete", map[string]any{"workspace": p.Slug, "confirm": p.Slug}, "")
	if d["deleted"] != p.ID || d["here"] != true {
		t.Fatalf("workspace_delete: %v", d)
	}

	// Every other tool refuses, with how to restore.
	msg := "workspace " + p.Slug + " was deleted on gears-api (restore: gears workspace restore)"
	for _, tool := range []string{"status", "card_list", "desk_list", "session_list", "artifact_list", "runbook_list", "members_list", "people_list"} {
		k.call(tool, nil, msg)
	}
	k.call("session_new", map[string]any{"kind": "done", "text": "x"}, msg)
	if _, err := verbs.Status(verbs.Ctx{Project: p}, verbs.StatusIn{}); !errors.Is(err, store.ErrWorkspaceDeleted) {
		t.Fatalf("verbs.Status: %v", err)
	}
	// The data is kept.
	db, err := store.OpenAny(p)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM session_entries`).Scan(&n)
	db.Close()
	if n != 1 {
		t.Fatalf("session entries kept: %d", n)
	}

	l := k.call("workspace_list", map[string]any{"deleted": true}, "")
	del := l["deleted"].([]any)
	if len(del) != 1 || del[0].(map[string]any)["id"] != p.ID || del[0].(map[string]any)["deletedBy"] == "" {
		t.Fatalf("workspace_list deleted: %v", l)
	}
	f.MeRole = "member"
	k.call("workspace_restore", nil, "only a workspace owner")
	f.MeRole = ""
	r := k.call("workspace_restore", nil, "")
	if r["here"] != true || r["workspace"].(map[string]any)["id"] != p.ID {
		t.Fatalf("workspace_restore: %v", r)
	}
	if st := k.call("status", nil, ""); st["projectId"] != p.ID {
		t.Fatalf("status after restore: %v", st)
	}
	k.call("session_list", nil, "")
}
