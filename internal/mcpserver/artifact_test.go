package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// artifact_delete removes the row and the file together: reconcile has
// nothing to regenerate or adopt afterwards, and a tombstone waits for sync.
func TestArtifactDeleteOverMCP(t *testing.T) {
	dir := t.TempDir()
	res, err := project.Init(dir, "arttest", "AT")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	srv := New(verbs.Ctx{Project: p, Agent: "tester"}, "test")
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

	call := func(name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var out map[string]any
		if len(r.Content) > 0 {
			if tc, ok := r.Content[0].(*mcp.TextContent); ok {
				_ = json.Unmarshal([]byte(tc.Text), &out)
			}
		}
		return r, out
	}
	text := func(r *mcp.CallToolResult) string {
		if len(r.Content) == 0 {
			return ""
		}
		tc, _ := r.Content[0].(*mcp.TextContent)
		if tc == nil {
			return ""
		}
		return tc.Text
	}

	_, a := call("artifact_new", map[string]any{"prefix": "free", "title": "Agent handoff"})
	_, b := call("artifact_new", map[string]any{"prefix": "idea", "title": "Keep me"})
	aID, aFile := a["id"].(string), a["file"].(string)
	aPath := filepath.Join(p.ArtifactsDir(), aFile)
	if _, err := os.Stat(aPath); err != nil {
		t.Fatalf("artifact file not written: %v", err)
	}

	// Refused: empty key, unknown key.
	if r, _ := call("artifact_delete", map[string]any{"artifact": ""}); !r.IsError {
		t.Fatalf("empty key should be refused")
	}
	if r, _ := call("artifact_delete", map[string]any{"artifact": "nope--x.md"}); !r.IsError || !strings.Contains(text(r), "no artifact") {
		t.Fatalf("unknown key: %v %s", r.IsError, text(r))
	}

	// Leading digits are a v7 timestamp; shared by both, they are refused.
	if bID := b["id"].(string); bID[:8] == aID[:8] {
		if r, _ := call("artifact_delete", map[string]any{"artifact": aID[:8]}); !r.IsError || !strings.Contains(text(r), "matches 2") {
			t.Fatalf("ambiguous key: %v %s", r.IsError, text(r))
		}
	}

	// By short id: the random tail listings show.
	r, out := call("artifact_delete", map[string]any{"artifact": aID[len(aID)-8:]})
	if r.IsError {
		t.Fatalf("artifact_delete: %s", text(r))
	}
	if out["id"] != aID || out["file"] != aFile {
		t.Fatalf("deleted the wrong artifact: %v", out)
	}
	if _, err := os.Stat(aPath); !os.IsNotExist(err) {
		t.Fatalf("file still on disk: %v", err)
	}

	// Reconcile finds nothing to regenerate or adopt; the list holds only b.
	if _, out = call("artifact_reconcile", nil); len(out["changes"].([]any)) != 0 {
		t.Fatalf("reconcile after delete: %v", out["changes"])
	}
	_, out = call("artifact_list", nil)
	list := out["artifacts"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != b["id"] {
		t.Fatalf("list after delete: %v", list)
	}

	// The tombstone waits for the next sync push.
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	var tomb int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_deletes WHERE tbl = 'artifacts' AND id = ?`, aID).Scan(&tomb)
	db.Close()
	if tomb != 1 {
		t.Fatalf("tombstone rows: %d", tomb)
	}

	// By file name, through a path.
	if r, _ = call("artifact_delete", map[string]any{"artifact": filepath.Join(".gears", "artifacts", b["file"].(string))}); r.IsError {
		t.Fatalf("delete by path: %s", text(r))
	}
	if _, out = call("artifact_list", nil); len(out["artifacts"].([]any)) != 0 {
		t.Fatalf("list not empty: %v", out["artifacts"])
	}
}

// GRS-44 under GRS-40: the server runs inside projects/alpha of a workspace
// root. artifact_new tags the artifact alpha and writes it to the root's
// cache (no .gears under projects/); artifact_delete by file name and by
// full id removes row and file and leaves a tombstone for each.
func TestArtifactDeleteProjectTaggedOverMCP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	res, err := project.Init(root, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	alpha := filepath.Join(root, "projects", "alpha")
	if err := os.MkdirAll(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", alpha, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, err := project.Scan(p, nil); err != nil {
		t.Fatal(err)
	}
	cur := project.Current(p, alpha)
	if cur == nil {
		t.Fatal("projects/alpha is not a current project")
	}

	srv := New(verbs.Ctx{Project: p, Agent: "tester", Current: cur}, "test")
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
	call := func(name string, args map[string]any) verbs.Artifact {
		t.Helper()
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		tc, _ := r.Content[0].(*mcp.TextContent)
		if r.IsError {
			t.Fatalf("%s: %s", name, tc.Text)
		}
		var a verbs.Artifact
		if err := json.Unmarshal([]byte(tc.Text), &a); err != nil {
			t.Fatalf("%s: %v %s", name, err, tc.Text)
		}
		return a
	}

	a := call("artifact_new", map[string]any{"prefix": "free", "title": "Doomed one"})
	b := call("artifact_new", map[string]any{"prefix": "free", "title": "Doomed two"})
	for _, x := range []verbs.Artifact{a, b} {
		if x.ProjectID != cur.ID {
			t.Fatalf("artifact %s tagged %q, want alpha %s", x.File, x.ProjectID, cur.ID)
		}
		if _, err := os.Stat(filepath.Join(p.ArtifactsDir(), x.File)); err != nil {
			t.Fatalf("not in the root cache: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(alpha, ".gears")); !os.IsNotExist(err) {
		t.Fatalf("a .gears appeared under projects/alpha: %v", err)
	}

	if got := call("artifact_delete", map[string]any{"artifact": a.File}); got.ID != a.ID || got.ProjectID != cur.ID || got.FilePresent {
		t.Fatalf("delete by file: %+v", got)
	}
	if got := call("artifact_delete", map[string]any{"artifact": b.ID}); got.ID != b.ID {
		t.Fatalf("delete by id: %+v", got)
	}
	for _, x := range []verbs.Artifact{a, b} {
		if _, err := os.Stat(filepath.Join(p.ArtifactsDir(), x.File)); !os.IsNotExist(err) {
			t.Fatalf("%s still on disk: %v", x.File, err)
		}
	}

	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if tc, _ := r.Content[0].(*mcp.TextContent); strings.Contains(tc.Text, "doomed") {
		t.Fatalf("list still has a deleted artifact: %s", tc.Text)
	}
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tombs int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sync_deletes WHERE tbl = 'artifacts' AND id IN (?, ?)`, a.ID, b.ID).Scan(&tombs)
	if tombs != 2 {
		t.Fatalf("tombstones: %d, want 2", tombs)
	}
}
