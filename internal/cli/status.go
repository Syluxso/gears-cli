package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var statusFull bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Cold-start packet for the whole workspace",
	Long: `Everything an agent needs before it starts: every project in the
workspace (from .gears/projects.yaml, the directories under projects/ and,
when logged in, gears-api) with its tech, branch, last commit, dirty files
and README line; open desk items, cards, active runs, artifacts and whether
they pass their shapes, and the latest session entries. Run it first in
every session. Inside projects/<repo> it is the same report, with that
project marked ▸. It never writes the manifest: a repo the manifest does not
list is reported as unlisted, for gears projects scan. Nothing is fetched;
ahead/behind are as of the last fetch.

The text is a glance: each section is one header line with its count and at
most three lines, each cut to the terminal width, with the command that
shows the rest. --full prints every item. --json (and the MCP status tool)
always carry the full data.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.Status(c, verbs.StatusIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if statusFull {
			printStatusFull(out)
			return nil
		}
		w, colour := stdoutStyle()
		printStatusBrief(os.Stdout, out, w, colour)
		return nil
	},
}

func init() {
	statusCmd.Flags().BoolVar(&statusFull, "full", false, "print every desk item, card, problem and session entry")
	root.AddCommand(statusCmd)
}

// statusShown is how many lines a section lists before it points at the
// command that lists the rest.
const statusShown = 3

// fallbackWidth is used when stdout is not a terminal or its width is unknown.
const fallbackWidth = 100

// stdoutStyle is the width to cut lines to and whether to colour them:
// colour only on a terminal, never with NO_COLOR set or TERM=dumb.
func stdoutStyle() (width int, colour bool) {
	fd := int(os.Stdout.Fd())
	width = fallbackWidth
	if !term.IsTerminal(fd) {
		return width, false
	}
	if w, _, err := term.GetSize(fd); err == nil && w > 0 {
		width = w
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return width, false
	}
	return width, enableVT()
}

// printStatusBrief renders the glance. Only this renderer trims; the data
// it is handed is the full packet.
func printStatusBrief(w io.Writer, out *verbs.StatusOut, width int, colour bool) {
	st := style{on: colour}
	line := func(s string) { fmt.Fprintln(w, cut(s, width)) }
	// head is a section header: the label bold, the count line after it.
	head := func(label, rest string) {
		plain := cut(fmt.Sprintf("%-9s %s", label, rest), width)
		if len(plain) > len(label) && strings.HasPrefix(plain, label) {
			plain = st.bold(label) + plain[len(label):]
		}
		fmt.Fprintln(w, plain)
	}
	// item is one listed line: the key column coloured, the rest plain,
	// the whole cut to width before any colour goes in.
	item := func(key, rest string) {
		plain := cut("  "+key+"  "+oneLine(rest), width)
		k := "  " + key
		if strings.HasPrefix(plain, k) {
			plain = "  " + st.cyan(key) + plain[len(k):]
		}
		fmt.Fprintln(w, plain)
	}
	more := func(n int, what, cmd string) {
		if n > 0 {
			fmt.Fprintln(w, st.dim(cut(fmt.Sprintf("  +%d more%s: %s", n, what, cmd), width)))
		}
	}

	line(fmt.Sprintf("%-9s %s  %s  (cards %s-n)", bindingLabel(out), out.Project, out.ProjectID, out.Prefix))
	line(fmt.Sprintf("agent     %s  (from %s)", out.Agent.Label, agentSourceShort(out.Agent.Source)))
	line("  change: gears -a <name> | GEARS_AGENT=<name> (.mcp.json env for MCP) | gears login --label <name>")

	fmt.Fprintln(w)
	head("projects", projectsHeader(out))
	slugW := 0
	for _, p := range out.Projects {
		slugW = max(slugW, utf8.RuneCountInString(p.Slug))
	}
	now := time.Now()
	for _, p := range out.Projects {
		mark := " "
		if p.Current {
			mark = "▸"
		}
		item(fmt.Sprintf("%s %-*s", mark, slugW, p.Slug), projectGlance(p, now))
	}

	fmt.Fprintln(w)
	head("desk", fmt.Sprintf("%d open", len(out.Desk)))
	for i := len(out.Desk) - 1; i >= 0 && i >= len(out.Desk)-statusShown; i-- {
		it := out.Desk[i]
		item(tail(it.ID), fmt.Sprintf("%-8s %s → %s  %s", it.Type, it.From, orAnyone(it.To), it.Text))
	}
	more(len(out.Desk)-statusShown, "", "gears desk list")

	fmt.Fprintln(w)
	var parts []string
	ready := ""
	for _, s := range out.Cards.ByState {
		parts = append(parts, fmt.Sprintf("%s %d", s.Name, s.Count))
		if s.Default {
			ready = s.Name
		}
	}
	states := ""
	if len(parts) > 0 {
		states = "  (" + strings.Join(parts, ", ") + ")"
	}
	head("cards", fmt.Sprintf("%d open, %d mine, %d ready%s", out.Cards.Open, len(out.Cards.Mine), len(out.Cards.Ready), states))
	for i, cd := range out.Cards.Mine {
		if i == statusShown {
			break
		}
		item(fmt.Sprintf("mine   %-8s", cd.Key), fmt.Sprintf("%-6s %-2s %-12s %s", cd.Type, cd.Priority, cd.State, cd.Title))
	}
	more(len(out.Cards.Mine)-statusShown, " mine", "gears card list --assignee me")
	for i, cd := range out.Cards.Ready {
		if i == statusShown {
			break
		}
		item(fmt.Sprintf("ready  %-8s", cd.Key), fmt.Sprintf("%-6s %-2s %s", cd.Type, cd.Priority, cd.Title))
	}
	more(len(out.Cards.Ready)-statusShown, " ready", "gears card list --state "+shellQuote(ready))

	fmt.Fprintln(w)
	head("runs", fmt.Sprintf("%d active", len(out.Runs)))
	for i, r := range out.Runs {
		if i == statusShown {
			break
		}
		s := runSummaryLine(r)
		k, rest, _ := strings.Cut(s, "  ")
		item(k, rest)
	}
	more(len(out.Runs)-statusShown, "", "gears runbook list")

	fmt.Fprintln(w)
	parts = nil
	for _, pre := range artifact.Prefixes {
		if n := out.Artifacts.ByPrefix[pre]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", pre, n))
		}
	}
	arts := fmt.Sprintf("%d, %d with problems", out.Artifacts.Total, len(out.Artifacts.Problems))
	if len(parts) > 0 {
		arts += "  (" + strings.Join(parts, ", ") + ")"
	}
	head("artifacts", arts)
	for i, s := range out.Artifacts.Problems {
		if i == statusShown {
			break
		}
		file, why, _ := strings.Cut(s, ": ")
		item("! "+file, why)
	}
	more(len(out.Artifacts.Problems)-statusShown, "", "gears artifact validate")

	fmt.Fprintln(w)
	head("session", fmt.Sprintf("%d today", len(out.Session)))
	from := len(out.Session) - statusShown
	if from < 0 {
		from = 0
	}
	for _, e := range out.Session[from:] {
		item(e.Short, fmt.Sprintf("%s  %-9s %-8s %s", hhmm(e.At), e.Agent, e.Kind, e.Text))
	}
	more(from, " earlier", "gears session list --days 1")

	fmt.Fprintln(w)
	head("next", out.NextAction)
}

// printStatusFull is the untrimmed text: every item, uncut, no colour.
func printStatusFull(out *verbs.StatusOut) {
	fmt.Printf("%-9s %s  %s  (cards %s-n)\n", bindingLabel(out), out.Project, out.ProjectID, out.Prefix)
	printAgent(os.Stdout, out.Agent)
	fmt.Printf("root      %s\n", out.Root)

	fmt.Printf("\nprojects  %s\n", projectsHeader(out))
	if out.API.Error != "" {
		fmt.Printf("  api error: %s\n", out.API.Error)
	}
	now := time.Now()
	for _, p := range out.Projects {
		mark := " "
		if p.Current {
			mark = "▸"
		}
		fmt.Printf("%s %s  %s  %s\n", mark, p.Slug, orDash(p.Path), orDash(p.ID))
		row := func(k, v string) {
			if v != "" {
				fmt.Printf("    %-7s %s\n", k, v)
			}
		}
		if p.Name != p.Slug {
			row("name", p.Name)
		}
		row("remote", p.Remote)
		if p.OnDisk {
			row("tech", strings.TrimSpace(p.Tech.String()+"  "+p.Tech.Detail))
		}
		if g := p.Git; g != nil {
			br := g.Branch
			if g.Detached {
				br = "(detached)"
			}
			if g.Upstream != "" {
				br += fmt.Sprintf(" → %s  ahead %d, behind %d", g.Upstream, g.Ahead, g.Behind)
			} else {
				br += "  (no upstream)"
			}
			row("branch", br)
			if g.Head != "" {
				row("commit", fmt.Sprintf("%s  %s  (%s)", g.Head, g.Subject, ago(parseTS(g.CommittedAt), now)))
			} else {
				row("commit", "(no commits)")
			}
			row("dirty", fmt.Sprintf("%d file%s", g.Dirty, plural(g.Dirty, "", "s")))
		}
		row("readme", p.Readme)
		if p.OnAPI != nil {
			row("api", map[bool]string{true: "on gears-api", false: "not on gears-api"}[*p.OnAPI])
		}
		for _, n := range p.Notes {
			row("note", n)
		}
	}

	fmt.Printf("\ndesk      %d open item%s\n", len(out.Desk), plural(len(out.Desk), "", "s"))
	for _, it := range out.Desk {
		fmt.Printf("  %s  %-8s %s → %s  %s\n", tail(it.ID), it.Type, it.From, orAnyone(it.To), it.Text)
	}

	if out.Cards.Open > 0 {
		var parts []string
		for _, s := range out.Cards.ByState {
			parts = append(parts, fmt.Sprintf("%s %d", s.Name, s.Count))
		}
		fmt.Printf("\ncards     %d open  (%s)\n", out.Cards.Open, strings.Join(parts, ", "))
		for _, cd := range out.Cards.Mine {
			fmt.Printf("  mine   %-8s %-6s %-2s %-12s %s\n", cd.Key, cd.Type, cd.Priority, cd.State, cd.Title)
		}
		for _, cd := range out.Cards.Ready {
			fmt.Printf("  ready  %-8s %-6s %-2s %-12s %s\n", cd.Key, cd.Type, cd.Priority, "", cd.Title)
		}
	}

	if len(out.Runs) > 0 {
		fmt.Printf("\nruns      %d active\n", len(out.Runs))
		for _, r := range out.Runs {
			fmt.Print("  ")
			printRunSummary(r)
		}
	}

	fmt.Printf("\nartifacts %d", out.Artifacts.Total)
	if out.Artifacts.Total > 0 {
		var parts []string
		for _, pre := range artifact.Prefixes {
			if n := out.Artifacts.ByPrefix[pre]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", pre, n))
			}
		}
		fmt.Printf("  (%s)", strings.Join(parts, ", "))
	}
	fmt.Println()
	for _, s := range out.Artifacts.Problems {
		fmt.Println("  !", s)
	}

	fmt.Printf("\nsession   %d entr%s today\n", len(out.Session), plural(len(out.Session), "y", "ies"))
	for _, e := range out.Session {
		fmt.Printf("  %s  %-9s %-8s %s\n", hhmm(e.At), e.Agent, e.Kind, e.Text)
	}

	fmt.Printf("\nnext      %s\n", out.NextAction)
}

// cut shortens s to width runes, ending in an ellipsis when it had to cut.
// It counts runes, not bytes, so a multi-byte character is never split.
func cut(s string, width int) string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:width-1]), " ") + "…"
}

// oneLine folds newlines and tabs so a listed item stays on its line.
func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(s)
}

func shellQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t'\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// style is a few ANSI codes; off, every method returns its text unchanged.
// Bold, dim and two colours are all status needs, so no styling library.
type style struct{ on bool }

func (s style) wrap(code, t string) string {
	if !s.on || t == "" {
		return t
	}
	return "\x1b[" + code + "m" + t + "\x1b[0m"
}

func (s style) bold(t string) string { return s.wrap("1", t) }
func (s style) dim(t string) string  { return s.wrap("2", t) }
func (s style) cyan(t string) string { return s.wrap("36", t) }

func bindingLabel(out *verbs.StatusOut) string {
	if out.IsWorkspace {
		return "workspace"
	}
	return "project"
}

// projectsHeader is the count line: how many, what is off, the API.
func projectsHeader(out *verbs.StatusOut) string {
	var unlisted, missing int
	for _, p := range out.Projects {
		switch {
		case !p.OnDisk && p.Listed:
			missing++
		case p.OnDisk && !p.Listed:
			unlisted++
		}
	}
	s := fmt.Sprintf("%d", len(out.Projects))
	if unlisted > 0 {
		s += fmt.Sprintf(", %d unlisted", unlisted)
	}
	if missing > 0 {
		s += fmt.Sprintf(", %d missing", missing)
	}
	s += "  api: " + out.API.State
	if out.API.State == verbs.APIOnline {
		s += fmt.Sprintf(" (%d project%s)", out.API.Projects, plural(out.API.Projects, "", "s"))
	}
	return s
}

// projectGlance is one project's line after its slug: tech, branch, dirty
// count, last commit age, ahead/behind, and the first note.
func projectGlance(p verbs.StatusProject, now time.Time) string {
	tech := "-"
	if p.OnDisk {
		tech = p.Tech.String()
	}
	parts := []string{fmt.Sprintf("%-13s", tech)}
	if g := p.Git; g != nil {
		br := g.Branch
		if g.Detached {
			br = "(detached)"
		}
		dirty := "clean"
		if g.Dirty > 0 {
			dirty = fmt.Sprintf("%d dirty", g.Dirty)
		}
		age := "no commits"
		if g.Head != "" {
			age = ago(parseTS(g.CommittedAt), now)
		}
		parts = append(parts, fmt.Sprintf("%-10s %-8s %-10s", br, dirty, age))
		if g.Ahead > 0 || g.Behind > 0 {
			parts = append(parts, fmt.Sprintf("↑%d ↓%d", g.Ahead, g.Behind))
		}
	}
	if len(p.Notes) > 0 {
		parts = append(parts, p.Notes[0])
	}
	return strings.TrimRight(strings.Join(parts, " "), " ")
}

// ago is a short relative age: 40s ago, 5m ago, 3h ago, 2d ago, 3w ago,
// 4mo ago, 2y ago.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orAnyone(to string) string {
	if to == "" {
		return "anyone"
	}
	return to
}

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// agentSourceShort names where the agent label came from, for one line.
func agentSourceShort(source string) string {
	switch source {
	case "flag":
		return "-a flag"
	case "env":
		return "GEARS_AGENT"
	case "login":
		return "gears login --label"
	}
	return "default; nothing set"
}

// printAgent is the agent block of status --full and whoami (GRS-21): the
// label, where it came from, and every way to change it.
func printAgent(w io.Writer, a verbs.AgentInfo) {
	fmt.Fprintf(w, "agent     %s  (from %s)\n", a.Label, a.SourceText)
	fmt.Fprintln(w, "  to change it:")
	for _, h := range a.HowToChange {
		fmt.Fprintln(w, "    "+h)
	}
}
