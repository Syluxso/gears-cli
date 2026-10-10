package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/session"
)

var (
	sessionKind  string
	sessionAgent string
	sessionDays  int
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "The project's log between runs",
}

var sessionNewCmd = &cobra.Command{
	Use:   "new <text>",
	Short: "Append an entry to today's session log",
	Long: `Append one entry to the session log. Entries are timestamped and fold
into one timeline with every other agent's entries.

Kinds:
  done      something finished; name the files or the card
  decision  a choice made; if architectural, also write an ADR
  problem   something that broke or blocked, and how it was resolved
  next      where the next agent starts, and what to avoid

  gears session new --kind done "Scaffolded gears-cli: init, status, artifact, session"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		e, err := session.Append(p, sessionKind, sessionAgent, strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Printf("%s  %s  %s  %s\n", e.At.Format("2006-01-02 15:04"), e.Agent, e.Kind, e.Text)
		return nil
	},
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show recent session entries, oldest first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		entries, err := session.Recent(p, sessionDays)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("no entries")
			return nil
		}
		day := ""
		for _, e := range entries {
			if d := e.At.Format("2006-01-02"); d != day {
				day = d
				fmt.Printf("\n%s\n", day)
			}
			fmt.Printf("  %s  %-9s %-8s %s\n", e.At.Format("15:04"), e.Agent, e.Kind, e.Text)
		}
		return nil
	},
}

func init() {
	sessionNewCmd.Flags().StringVarP(&sessionKind, "kind", "k", "done", "done | decision | problem | next")
	sessionNewCmd.Flags().StringVarP(&sessionAgent, "agent", "a", "", "agent label (default: $GEARS_AGENT or \"local\")")
	sessionListCmd.Flags().IntVar(&sessionDays, "days", 3, "how many days back to show")
	sessionCmd.AddCommand(sessionNewCmd, sessionListCmd)
	root.AddCommand(sessionCmd)
}
