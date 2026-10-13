package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-40: projects_scan and projects_list over MCP, and session_new tagged
// with a project named from the workspace root.
func TestProjectsToolsOverMCP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	res, err := project.Init(root, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	alpha := filepath.Join(root, "projects", "alpha")
	if err := os.MkdirAll(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:example/alpha.git"}} {
		c := exec.Command("git", args...)
		c.Dir = alpha
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	srv := New(verbs.Ctx{Project: res.Project, Agent: "tester"}, "test")
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any, out any) {
		t.Helper()
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		tc, _ := r.Content[0].(*mcp.TextContent)
		if r.IsError {
			t.Fatalf("%s: %s", name, tc.Text)
		}
		if err := json.Unmarshal([]byte(tc.Text), out); err != nil {
			t.Fatalf("%s: %v %s", name, err, tc.Text)
		}
	}

	var scan verbs.ProjectsScanOut
	call("projects_scan", map[string]any{"offline": true}, &scan)
	if len(scan.Added) != 1 || scan.Added[0] != "projects/alpha" || len(scan.Projects) != 1 {
		t.Fatalf("scan: %+v", scan)
	}
	var list verbs.ProjectsListOut
	call("projects_list", map[string]any{}, &list)
	if len(list.Projects) != 1 || list.Projects[0].Slug != "alpha" || list.Projects[0].Remote != "git@github.com:example/alpha.git" {
		t.Fatalf("list: %+v", list)
	}
	var line verbs.SessionLine
	call("session_new", map[string]any{"kind": "done", "text": "alpha work", "project": "alpha"}, &line)
	if line.ProjectID != list.Projects[0].ID {
		t.Fatalf("session entry project %q, want %s", line.ProjectID, list.Projects[0].ID)
	}
}

// GRS-46: the status tool is the workspace report: a projects array with
// tech, git and notes, the current project marked, and the API state. The
// home directory is a temp dir, so no saved token: "not logged in".
func TestStatusToolReportsWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", "")
	root := t.TempDir()
	res, err := project.Init(root, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, "projects", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "projects", "alpha", "go.mod"), []byte("module example.com/alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Scan(res.Project, nil); err != nil {
		t.Fatal(err)
	}
	cur := project.Current(res.Project, filepath.Join(root, "projects", "beta"))
	if cur == nil {
		t.Fatal("no current project for projects/beta")
	}

	srv := New(verbs.Ctx{Project: res.Project, Agent: "tester", Current: cur}, "test")
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	tc, _ := r.Content[0].(*mcp.TextContent)
	if r.IsError {
		t.Fatal(tc.Text)
	}
	var out verbs.StatusOut
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("%v %s", err, tc.Text)
	}
	if out.API.State != verbs.APINotLoggedIn {
		t.Errorf("api: %+v", out.API)
	}
	if len(out.Projects) != 2 {
		t.Fatalf("projects: %s", tc.Text)
	}
	a, b := out.Projects[0], out.Projects[1]
	if a.Slug != "alpha" || a.Tech.Kind != "Go" || a.Current || a.Git == nil || !a.Listed {
		t.Errorf("alpha: %+v", a)
	}
	if b.Slug != "beta" || !b.Current || b.Tech.Kind != "empty" || b.Git == nil {
		t.Errorf("beta: %+v", b)
	}
}
