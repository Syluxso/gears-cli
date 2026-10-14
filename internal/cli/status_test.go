package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

func bigStatus() *verbs.StatusOut {
	out := &verbs.StatusOut{Project: "gears", ProjectID: "01a1-proj", Prefix: "GRS", Agent: verbs.AgentInfo{Label: "claude-local", Source: "env", SourceText: "the GEARS_AGENT environment variable", HowToChange: verbs.AgentHowToChange()},
		Cards:     verbs.CardStats{Open: 10, ByState: []verbs.CardState{{Name: "Backlog", Count: 6, Default: true}, {Name: "In Progress", Count: 4}}},
		Artifacts: verbs.ArtifactStats{Total: 5, ByPrefix: map[string]int{"free": 5}},
		Runs:      []verbs.RunSummary{}, NextAction: "work the desk: 16 open item(s)", IsWorkspace: true,
		API: verbs.StatusAPI{State: verbs.APIOffline, Error: "unreachable"}}
	hour := time.Now().Add(-3 * time.Hour).Format(time.RFC3339)
	out.Projects = []verbs.StatusProject{
		{Slug: "gears-api", Path: "projects/gears-api", Listed: true, OnDisk: true, Tech: project.Tech{Kind: "Go", Detail: "github.com/Syluxso/gears-api"},
			Git: &verbs.StatusGit{Branch: "main", Upstream: "origin/main", Ahead: 2, Dirty: 4, Head: "abc1234", Subject: "x", CommittedAt: hour}, Notes: []string{}},
		{Slug: "gears-cli", Path: "projects/gears-cli", Current: true, Listed: true, OnDisk: true, Tech: project.Tech{Kind: "Go"},
			Git: &verbs.StatusGit{Branch: "main", Head: "def5678", Subject: "y", CommittedAt: hour}, Notes: []string{}},
		{Slug: "gears-new", Path: "projects/gears-new", OnDisk: true, Tech: project.Tech{Kind: "Node/Angular"},
			Git: &verbs.StatusGit{Branch: "master"}, Notes: []string{"unlisted, run gears projects scan"}},
		{Slug: "gears-old", Path: "projects/gears-old", Listed: true, Notes: []string{"missing: in projects.yaml, not on disk"}},
	}
	for i := 0; i < 16; i++ {
		out.Desk = append(out.Desk, verbs.DeskItem{ID: fmt.Sprintf("x-d%07d", i), Type: "handoff", From: "a", Text: fmt.Sprintf("item %d ", i) + strings.Repeat("é", 200)})
	}
	for i := 0; i < 4; i++ {
		out.Cards.Mine = append(out.Cards.Mine, verbs.CardLine{Key: fmt.Sprintf("GRS-%d", i), Type: "story", State: "In Progress", Priority: "P2", Title: "mine"})
	}
	for i := 0; i < 6; i++ {
		out.Cards.Ready = append(out.Cards.Ready, verbs.CardLine{Key: fmt.Sprintf("GRS-%d", 10+i), Type: "task", Priority: "P1", Title: "ready"})
	}
	for i := 0; i < 5; i++ {
		out.Artifacts.Problems = append(out.Artifacts.Problems, fmt.Sprintf("free--%d.md: missing section", i))
	}
	for i := 0; i < 7; i++ {
		out.Session = append(out.Session, verbs.SessionLine{ID: "x", Short: fmt.Sprintf("s%07d", i), At: "2026-10-12T10:00:00Z", Agent: "a", Kind: "done", Text: "line\nwith a break"})
	}
	return out
}

// The glance: one header per section, three lines, the rest named by a
// command, every line one line no wider than the width, no colour unless
// asked.
func TestStatusBrief(t *testing.T) {
	var b bytes.Buffer
	printStatusBrief(&b, bigStatus(), 80, false)
	got := b.String()
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("colour in plain output")
	}
	for _, l := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if n := utf8.RuneCountInString(l); n > 80 {
			t.Errorf("line is %d runes: %q", n, l)
		}
		if !utf8.ValidString(l) {
			t.Errorf("line split a rune: %q", l)
		}
	}
	for _, want := range []string{
		"workspace gears  01a1-proj  (cards GRS-n)",
		"projects  4, 1 unlisted, 1 missing  api: offline",
		"    gears-api  Go            main       4 dirty  3h ago     ↑2 ↓0",
		"  ▸ gears-cli  Go            main       clean    3h ago",
		"    gears-new  Node/Angular  master     clean    no commits unlisted, run gears…",
		"    gears-old  -             missing: in projects.yaml, not on disk",
		"desk      16 open",
		"+13 more: gears desk list",
		"cards     10 open, 4 mine, 6 ready  (Backlog 6, In Progress 4)",
		"+1 more mine: gears card list --assignee me",
		"+3 more ready: gears card list --state Backlog",
		"runs      0 active",
		"artifacts 5, 5 with problems  (free 5)",
		"+2 more: gears artifact validate",
		"session   7 today",
		"+4 more earlier: gears session list --days 1",
		"next      work the desk: 16 open item(s)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Desk shows the three newest, newest first.
	if !strings.Contains(got, "d0000015") || strings.Contains(got, "d0000012") {
		t.Errorf("desk is not the three newest:\n%s", got)
	}
	if i, j := strings.Index(got, "d0000015"), strings.Index(got, "d0000013"); i > j {
		t.Errorf("desk not newest first")
	}
	// Session shows the last three; a newline in the text stays on its line.
	if !strings.Contains(got, "s0000006") || strings.Contains(got, "s0000003") || !strings.Contains(got, "line with a break") {
		t.Errorf("session lines:\n%s", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("long desk text was not cut with an ellipsis")
	}

	b.Reset()
	printStatusBrief(&b, bigStatus(), 80, true)
	if !strings.Contains(b.String(), "\x1b[1mdesk\x1b[0m") {
		t.Errorf("no bold header with colour on:\n%q", b.String())
	}
}

func TestCut(t *testing.T) {
	if got := cut("héllo wörld", 6); got != "héllo…" {
		t.Errorf("cut: %q", got)
	}
	if got := cut("short", 10); got != "short" {
		t.Errorf("cut: %q", got)
	}
}

// GRS-21: the glance names the agent, its source and how to change it; the
// full block (status --full, whoami) lists every way.
func TestStatusAgentLines(t *testing.T) {
	var b bytes.Buffer
	printStatusBrief(&b, bigStatus(), 200, false)
	got := b.String()
	for _, want := range []string{"agent     claude-local  (from GEARS_AGENT)", "change: gears -a <name>", "gears login --label <name>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("brief lacks %q:\n%s", want, got)
		}
	}
	b.Reset()
	printAgent(&b, bigStatus().Agent)
	got = b.String()
	for _, want := range []string{"agent     claude-local  (from the GEARS_AGENT environment variable)", "to change it:", "gears -a <name> <command>", "export GEARS_AGENT=<name>", "set GEARS_AGENT=<name>", ".mcp.json", "gears login --label <name>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent block lacks %q:\n%s", want, got)
		}
	}
}
