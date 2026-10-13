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
