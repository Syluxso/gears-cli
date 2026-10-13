package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/card"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/sync"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-51: gears run holds the board stream open beside the MCP server. A
// pull in flight (here, a slow API) must not hold up tool calls; card
// writes answer with sync.pushed and the API's key; an edit made elsewhere
// lands in the cache the tools read.
func TestToolsAnswerWhileStreamRuns(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	fakeapi.Ready(t, p)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sync.Live(ctx, sync.LiveOptions{Client: api.Anonymous(srv.URL), Project: p, Logf: t.Logf})
	}()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(2 * time.Second)
	for f.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if f.Subscribers() != 1 {
		t.Fatal("stream not open")
	}

	s := New(verbs.Ctx{Project: p, Agent: "claude-local"}, "test")
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || r.IsError {
			t.Fatalf("%s: %v %+v", name, err, r)
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &out)
		return out
	}

	out := call("card_new", map[string]any{"type": "task", "title": "Over MCP", "sections": map[string]any{"task": map[string]any{"what": "x", "soThat": "y"}},
		"acceptance": []string{"z"}})
	if out["key"] != "WS-1" || out["sync"].(map[string]any)["pushed"] != true {
		t.Fatalf("card_new: %v", out)
	}
	id := out["id"].(string)

	// Every pull now takes 1.5s. An edit elsewhere starts one; the tools
	// keep answering meanwhile.
	f.SetPullDelay(1500 * time.Millisecond)
	f.Edit("cards", id, "browser", map[string]any{"stateId": f.StateID("Testing"), "state": "Testing"})
	time.Sleep(400 * time.Millisecond) // debounce passed: the pull is in flight
	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"card_list", map[string]any{}},
		{"card_comment", map[string]any{"key": "WS-1", "body": "while the stream pulls"}},
		{"card_show", map[string]any{"key": "WS-1"}},
		{"desk_list", map[string]any{}},
	} {
		start := time.Now()
		res := call(tool.name, tool.args)
		if d := time.Since(start); d > time.Second {
			t.Fatalf("%s took %s while a stream pull was in flight", tool.name, d)
		}
		if tool.name == "card_comment" && res["sync"].(map[string]any)["pushed"] != true {
			t.Fatalf("card_comment: %v", res)
		}
	}
	f.SetPullDelay(0)
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := card.Get(p, id); c != nil && c.State == "Testing" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the edit made elsewhere never reached the cache")
}
