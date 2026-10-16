package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/runs"
)

// GRS-73: the local MCP exposes every runbook tool of the remote MCP with
// the same name, required args and args (identity args workspace and
// agent aside), per testdata/runbook-tools.json, which is the same file as
// gears-api's testdata/runbook-tools.json. Beyond them it has only the
// localOnly draft tools, and none of the retired ones.

type runbookParity struct {
	Tools map[string]struct {
		Required []string `json:"required"`
		Args     []string `json:"args"`
	} `json:"tools"`
	LocalOnly []string `json:"localOnly"`
	Retired   []string `json:"retired"`
}

func toolArgs(tl *mcp.Tool) (required, args []string) {
	raw, _ := json.Marshal(tl.InputSchema)
	var sch struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(raw, &sch)
	required, args = []string{}, []string{}
	for _, r := range sch.Required {
		if r != "workspace" && r != "agent" {
			required = append(required, r)
		}
	}
	for k := range sch.Properties {
		if k != "workspace" && k != "agent" {
			args = append(args, k)
		}
	}
	sort.Strings(required)
	sort.Strings(args)
	return required, args
}

func TestRunbookToolParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/runbook-tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var want runbookParity
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	p := fakeapi.Workspace(t)
	tc := connectAs(t, p, "claude-local")
	tools, err := tc.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		if strings.HasPrefix(tl.Name, "runbook_") {
			got[tl.Name] = tl
		}
	}
	for name, w := range want.Tools {
		tl := got[name]
		if tl == nil {
			t.Errorf("local MCP lacks %s", name)
			continue
		}
		req, args := toolArgs(tl)
		wr, wa := append([]string{}, w.Required...), append([]string{}, w.Args...)
		sort.Strings(wr)
		sort.Strings(wa)
		if !reflect.DeepEqual(req, wr) || !reflect.DeepEqual(args, wa) {
			t.Errorf("%s: required %v args %v, want required %v args %v", name, req, args, wr, wa)
		}
	}
	local := map[string]bool{}
	for _, n := range want.LocalOnly {
		local[n] = true
	}
	for name := range got {
		if _, ok := want.Tools[name]; !ok && !local[name] {
			t.Errorf("local MCP has %s, which is neither a shared tool nor localOnly in testdata/runbook-tools.json", name)
		}
	}
	for _, n := range want.Retired {
		if got[n] != nil {
			t.Errorf("retired tool %s is still served", n)
		}
	}
	// The shared list must be gears-api's: same file in both repos.
	if api, err := os.ReadFile("../../../gears-api/testdata/runbook-tools.json"); err == nil {
		norm := func(b []byte) string { return strings.ReplaceAll(string(b), "\r\n", "\n") }
		if norm(api) != norm(raw) {
			t.Error("testdata/runbook-tools.json differs from gears-api's copy; keep them identical")
		}
	}
}

func TestRunbookUnadoptLocal(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	me := connectAs(t, p, "claude-local")

	me.must("runbook_adopt", map[string]any{"ref": "single-ticket-development"})
	if rb, _ := runs.Runbook(p, "single-ticket-development"); rb == nil {
		t.Fatal("not cached")
	}
	f.RunbooksOff = true // un-adopting needs no orchestrator
	out := me.must("runbook_unadopt", map[string]any{"runbook": "single-ticket-development"})
	if out["deleted"] != true {
		t.Fatalf("unadopt: %v", out)
	}
	if rb, _ := runs.Runbook(p, "single-ticket-development"); rb != nil {
		t.Fatal("still cached")
	}
	if l := me.must("runbook_list", map[string]any{}); len(l["adopted"].([]any)) != 0 {
		t.Fatalf("list after unadopt: %v", l)
	}
	me.mustFail("runbook_unadopt", map[string]any{"runbook": "single-ticket-development"}, "no adopted runbook")
}
