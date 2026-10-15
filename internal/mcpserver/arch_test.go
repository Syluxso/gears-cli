package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-41: the arch tool and the status tool both carry arch--primary.md in
// full; artifact_new prefix arch writes it once and refuses a second.
func TestArchOverMCP(t *testing.T) {
	dir := t.TempDir()
	res, err := project.Init(dir, "archtest", "AR")
	if err != nil {
		t.Fatal(err)
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

	_, a := call("arch", map[string]any{})
	if a["missing"] != true || a["hint"] == "" {
		t.Fatalf("arch before init: %v", a)
	}
	r, made := call("artifact_new", map[string]any{"prefix": "arch", "title": "Whatever"})
	if r.IsError || made["file"] != "arch--primary.md" {
		t.Fatalf("artifact_new arch: %v %v", r.IsError, made)
	}
	if r, _ := call("artifact_new", map[string]any{"prefix": "arch", "title": "Second"}); !r.IsError {
		t.Fatal("second arch: want a tool error")
	}
	_, a = call("arch", map[string]any{})
	body, _ := a["body"].(string)
	if a["file"] != "arch--primary.md" || body == "" || a["missing"] != nil {
		t.Fatalf("arch: %v", a)
	}
	_, s := call("status", map[string]any{})
	arch, _ := s["arch"].(map[string]any)
	if arch["file"] != "arch--primary.md" || arch["body"] != body {
		t.Fatalf("status arch: %v", arch)
	}
}
