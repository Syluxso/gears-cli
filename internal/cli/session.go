package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	sessionKind string
	sessionDays int
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "The project's log between runs",
}

var sessionNewCmd = &cobra.Command{
	Use:   "new <text>",
	Short: "Append an entry to today's session log",
	Long: `Append one entry to the session log. Entries are timestamped and fold
into one timeline with every other agent's entries. Prints the entry id
first so it can be used as runbook evidence.

Kinds:
  done      something finished; name the files or the card
  decision  a choice made; if architectural, also write an ADR
  problem   something that broke or blocked, and how it was resolved
  next      where the next agent starts, and what to avoid

  gears session new --kind done "Scaffolded gears-cli: init, status, artifact, session"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.SessionNew(c, verbs.SessionNewIn{Kind: sessionKind, Text: strings.Join(args, " ")})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("%d  %s  %s  %s  %s\n", out.ID, hhmm(out.At), out.Agent, out.Kind, out.Text)
		return nil
	},
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show recent session entries, oldest first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.SessionList(c, verbs.SessionListIn{Days: sessionDays})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Entries) == 0 {
			fmt.Println("no entries")
			return nil
		}
		day := ""
		for _, e := range out.Entries {
			if d := parseTS(e.At).Local().Format("2006-01-02"); d != day {
				day = d
				fmt.Printf("\n%s\n", day)
			}
			fmt.Printf("  %-4d %s  %-9s %-8s %s\n", e.ID, hhmm(e.At), e.Agent, e.Kind, e.Text)
		}
		return nil
	},
}

func init() {
	sessionNewCmd.Flags().StringVarP(&sessionKind, "kind", "k", "done", "done | decision | problem | next")
	sessionListCmd.Flags().IntVar(&sessionDays, "days", 3, "how many days back to show")
	sessionCmd.AddCommand(sessionNewCmd, sessionListCmd)
	root.AddCommand(sessionCmd)
}
