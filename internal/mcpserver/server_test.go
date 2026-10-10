package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// The MCP door is the primary one, so the card tools are exercised through
// it: schemas must build, map-typed sections must round-trip, and the
// refusal rule must come back as a tool error an agent can read.
func TestCardToolsOverMCP(t *testing.T) {
	dir := t.TempDir()
	res, err := project.Init(dir, "mcptest", "MT")
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

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"card_shapes", "card_new", "card_list", "card_show", "card_update", "card_move", "card_claim", "card_comment", "card_tag", "card_link", "card_checklist", "card_states", "card_activity", "card_delete", "export"} {
		if !names[want] {
			t.Errorf("tool %s missing", want)
		}
	}

	call := func(name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var out map[string]any
		if !r.IsError && len(r.Content) > 0 {
			if tc, ok := r.Content[0].(*mcp.TextContent); ok {
				_ = json.Unmarshal([]byte(tc.Text), &out)
			}
		}
		return r, out
	}
	errText := func(r *mcp.CallToolResult) string {
		if len(r.Content) == 0 {
			return ""
		}
		tc, _ := r.Content[0].(*mcp.TextContent)
		if tc == nil {
			return ""
		}
		return tc.Text
	}

	// Refused: no acceptance.
	r, _ := call("card_new", map[string]any{"title": "Half", "story": map[string]any{"asA": "a", "iWant": "b", "soThat": "c"}, "acceptance": []string{}})
	if !r.IsError || !strings.Contains(errText(r), "acceptance") {
		t.Fatalf("expected refusal naming acceptance, got %v %s", r.IsError, errText(r))
	}
	// Refused: unknown section field.
	r, _ = call("card_new", map[string]any{"type": "defect", "title": "Bad", "acceptance": []string{"x"},
		"sections": map[string]any{"defect": map[string]any{"symptom": "s", "expected": "e", "actual": "a", "severity": "high"}}})
	if !r.IsError || !strings.Contains(errText(r), "severity") {
		t.Fatalf("expected refusal naming severity, got %s", errText(r))
	}

	// Created, with a brief and a tag.
	r, out := call("card_new", map[string]any{"title": "Login with Google", "size": "S", "priority": "P2", "tags": []string{"Auth"},
		"story":      map[string]any{"asA": "staff member", "iWant": "to sign in with Google", "soThat": "I have no second password"},
		"acceptance": []string{"Google button shows, proven by a screenshot"},
		"sections":   map[string]any{"brief": map[string]any{"files": []string{"auth.go"}, "rules": []string{"NEVER store the IAM refresh token"}}}})
	if r.IsError {
		t.Fatalf("card_new: %s", errText(r))
	}
	if out["key"] != "MT-1" || out["priority"] != "P2" || out["size"] != "S" {
		t.Fatalf("card: %v", out)
	}
	if !strings.HasPrefix(out["topLine"].(string), "Story: As a staff member") {
		t.Fatalf("topLine: %v", out["topLine"])
	}

	// Defect via sections, blocked by the story.
	r, out = call("card_new", map[string]any{"type": "bug", "title": "Drafts fail", "acceptance": []string{"Send succeeds"},
		"sections": map[string]any{"defect": map[string]any{"symptom": "drafts fail", "expected": "sent", "actual": "500"}}})
	if r.IsError {
		t.Fatalf("defect: %s", errText(r))
	}
	if out["type"] != "defect" || out["key"] != "MT-2" {
		t.Fatalf("defect: %v", out)
	}
	if r, _ = call("card_link", map[string]any{"key": "MT-2", "kind": "blocked-by", "target": "MT-1"}); r.IsError {
		t.Fatalf("link: %s", errText(r))
	}
	r, full := call("card_show", map[string]any{"key": "2"})
	if r.IsError {
		t.Fatalf("show: %s", errText(r))
	}
	if bb, _ := full["blockedBy"].([]any); len(bb) != 1 || bb[0] != "MT-1" {
		t.Fatalf("blockedBy: %v", full["blockedBy"])
	}

	// Claim, comment, checklist, move to Completed.
	if r, out = call("card_claim", map[string]any{"key": "MT-1"}); r.IsError {
		t.Fatalf("claim: %s", errText(r))
	} else if c := out["card"].(map[string]any); c["assignee"] != "tester" || c["state"] != "In Progress" {
		t.Fatalf("claim: %v", c)
	}
	if r, _ = call("card_comment", map[string]any{"key": "MT-1", "body": "Tried the button; works."}); r.IsError {
		t.Fatalf("comment: %s", errText(r))
	}
	if r, out = call("card_checklist", map[string]any{"key": "MT-1", "op": "add", "items": []string{"Button shows", "Revoked refused"}}); r.IsError {
		t.Fatalf("checklist: %s", errText(r))
	}
	if r, out = call("card_checklist", map[string]any{"key": "MT-1", "op": "tick", "item": "1"}); r.IsError {
		t.Fatalf("tick: %s", errText(r))
	} else if cl := out["checklists"].([]any)[0].(map[string]any); cl["done"] != float64(1) {
		t.Fatalf("tick: %v", cl)
	}
	if r, out = call("card_move", map[string]any{"key": "MT-1", "state": "completed"}); r.IsError {
		t.Fatalf("move: %s", errText(r))
	} else if c := out["card"].(map[string]any); c["completedAt"] == nil || c["completedAt"] == "" {
		t.Fatalf("completedAt missing: %v", c)
	}

	// States: add with WIP, then overrun warns.
	if r, _ = call("card_states", map[string]any{"op": "add", "name": "Review", "wipLimit": 1}); r.IsError {
		t.Fatalf("states add: %s", errText(r))
	}
	call("card_move", map[string]any{"key": "MT-2", "state": "Review"})
	r, _ = call("card_new", map[string]any{"type": "chore", "title": "Prune", "acceptance": []string{"done"}, "sections": map[string]any{"chore": map[string]any{"what": "prune branches", "why": "readable repo"}}})
	if r.IsError {
		t.Fatalf("chore: %s", errText(r))
	}
	if r, out = call("card_move", map[string]any{"key": "MT-3", "state": "Review"}); r.IsError {
		t.Fatalf("move: %s", errText(r))
	} else if w, _ := out["warning"].(string); !strings.Contains(w, "WIP") {
		t.Fatalf("expected WIP warning, got %v", out)
	}

	// List hides completed; activity has the trail; status shows mine.
	_, board := call("card_list", map[string]any{})
	if cards := board["cards"].([]any); len(cards) != 2 {
		t.Fatalf("open cards: %d", len(cards))
	}
	_, act := call("card_activity", map[string]any{})
	if evs := act["events"].([]any); len(evs) < 8 {
		t.Fatalf("events: %d", len(evs))
	}
	_, status := call("status", map[string]any{})
	if cs := status["cards"].(map[string]any); cs["open"] != float64(2) {
		t.Fatalf("status cards: %v", cs)
	}
}
