package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-36 on the local MCP: people_list reads gears-api when logged in and
// caches; offline it answers the cache, stale. desk_send takes assignedTo
// (checked against the people), and desk_list shows an agent what is its
// person's: assigned to them, addressed to any of their labels, or to
// nobody.
func TestPeopleListAndAssignedDesk(t *testing.T) {
	var down atomic.Bool
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/people") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"me": "u-me", "people": []map[string]any{
			{"id": "u-me", "name": "Me", "email": "me@example.test", "role": "owner", "agentLabels": []string{"tester", "tester-grok"}},
			{"id": "u-bob", "name": "Bob", "email": "bob@example.test", "role": "member", "agentLabels": []string{"bob-claude"}},
		}})
	}))
	defer srv.Close()
	fakeapi.Login(t, srv.URL)

	res, err := project.Init(t.TempDir(), "peopletest", "PT")
	if err != nil {
		t.Fatal(err)
	}
	s := New(verbs.Ctx{Project: res.Project, Agent: "tester"}, "test")
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any, wantErr bool) map[string]any {
		t.Helper()
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := ""
		if len(r.Content) > 0 {
			text = r.Content[0].(*mcp.TextContent).Text
		}
		if r.IsError != wantErr {
			t.Fatalf("%s %v: isError %v: %s", name, args, r.IsError, text)
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(text), &out)
		return out
	}

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "desk_send" && !strings.Contains(tl.Description, "people_list") {
			t.Fatalf("desk_send description: %s", tl.Description)
		}
	}

	pl := call("people_list", nil, false)
	if pl["stale"] != false || pl["me"] != "u-me" || len(pl["people"].([]any)) != 2 {
		t.Fatalf("people_list: %v", pl)
	}

	call("desk_send", map[string]any{"type": "info", "text": "x", "assignedTo": "u-nobody"}, true)
	forBob := call("desk_send", map[string]any{"type": "work", "text": "hello world", "assignedTo": "u-bob"}, false)
	if forBob["assignedTo"] != "u-bob" {
		t.Fatalf("desk_send: %v", forBob)
	}
	forMe := call("desk_send", map[string]any{"type": "work", "text": "for me", "assignedTo": "u-me"}, false)
	toMyOther := call("desk_send", map[string]any{"type": "info", "text": "to my grok", "to": "tester-grok"}, false)
	anyone := call("desk_send", map[string]any{"type": "info", "text": "anyone"}, false)
	toBobAgent := call("desk_send", map[string]any{"type": "info", "text": "to bob's agent", "to": "bob-claude"}, false)

	seen := map[string]bool{}
	for _, it := range call("desk_list", nil, false)["items"].([]any) {
		seen[it.(map[string]any)["id"].(string)] = true
	}
	for _, it := range []map[string]any{forMe, toMyOther, anyone} {
		if !seen[it["id"].(string)] {
			t.Fatalf("desk_list misses %v: %v", it["text"], seen)
		}
	}
	if seen[forBob["id"].(string)] || seen[toBobAgent["id"].(string)] {
		t.Fatalf("desk_list shows bob's items: %v", seen)
	}

	// Offline: the cached list, stale.
	down.Store(true)
	pl = call("people_list", nil, false)
	if pl["stale"] != true || len(pl["people"].([]any)) != 2 || pl["note"] == "" {
		t.Fatalf("offline people_list: %v", pl)
	}
	if calls.Load() != 2 {
		t.Fatalf("api calls: %d", calls.Load())
	}
}
