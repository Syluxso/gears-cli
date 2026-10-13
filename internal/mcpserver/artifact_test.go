package mcpserver

import (
	"context"
	"encoding/json"
	"os"
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
