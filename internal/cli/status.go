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
	Short: "Cold-start packet for this project",
	Long: `Everything an agent needs before it starts: the bound project, open desk
items, active runs, artifacts and whether they pass their shapes, and the
latest session entries. Run it first in every session.

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

	line(fmt.Sprintf("project   %s  %s  (cards %s-n)", out.Project, out.ProjectID, out.Prefix))
	line(fmt.Sprintf("agent     %s", out.Agent))

	fmt.Fprintln(w)
	head("desk", fmt.Sprintf("%d open", len(out.Desk)))
	for i := len(out.Desk) - 1; i >= 0 && i >= len(out.Desk)-statusShown; i-- {
		it := out.Desk[i]
		item(short(it.ID), fmt.Sprintf("%-8s %s → %s  %s", it.Type, it.From, orAnyone(it.To), it.Text))
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
	fmt.Printf("project   %s  %s  (cards %s-n)\n", out.Project, out.ProjectID, out.Prefix)
	fmt.Printf("agent     %s\n", out.Agent)
	fmt.Printf("root      %s\n", out.Root)

	fmt.Printf("\ndesk      %d open item%s\n", len(out.Desk), plural(len(out.Desk), "", "s"))
	for _, it := range out.Desk {
		fmt.Printf("  %s  %-8s %s → %s  %s\n", short(it.ID), it.Type, it.From, orAnyone(it.To), it.Text)
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
