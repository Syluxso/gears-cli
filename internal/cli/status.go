package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/artifact"
	"github.com/Syluxso/gears/internal/desk"
	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/runbook"
	"github.com/Syluxso/gears/internal/session"
)

var statusAgent string

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Cold-start packet for this project",
	Long: `Everything an agent needs before it starts: the bound project, open desk
items, active runs, artifacts and whether they pass their shapes, and the
latest session entries. Run it first in every session.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		agent := statusAgent
		if agent == "" {
			agent = session.DefaultAgent()
		}

		fmt.Printf("project   %s\n", p.Slug)
		fmt.Printf("agent     %s\n", agent)
		fmt.Printf("root      %s\n", p.Root)

		// Desk first: it is what someone left for this agent.
		items, err := desk.List(p, desk.Filter{Agent: agent})
		if err != nil {
			return err
		}
		fmt.Printf("\ndesk      %d open item%s\n", len(items), plural(len(items), "", "s"))
		for _, it := range items {
			to := it.To
			if to == "" {
				to = "anyone"
			}
			fmt.Printf("  %s  %-8s %s → %s  %s\n", it.Short(), it.Type, it.From, to, it.Text)
		}

		// Active runs.
		runs, err := runbook.Active(p)
		if err != nil {
			return err
		}
		if len(runs) > 0 {
			fmt.Printf("\nruns      %d active\n", len(runs))
			for _, r := range runs {
				fmt.Print("  ")
				printRunLine(r)
			}
		}

		// Artifacts, reconciled, grouped by prefix, with problems.
		list, err := artifact.List(p)
		if err != nil {
			return err
		}
		counts := map[string]int{}
		var problems []string
		for _, a := range list {
			counts[a.Prefix]++
			switch {
			case !a.FilePresent:
				problems = append(problems, a.File+": file missing")
			case !a.ShapeOK:
				problems = append(problems, a.File+": "+strings.Join(a.ShapeErrors, "; "))
			}
		}
		fmt.Printf("\nartifacts %d", len(list))
		if len(list) > 0 {
			var parts []string
			for _, pre := range artifact.Prefixes {
				if n := counts[pre]; n > 0 {
					parts = append(parts, fmt.Sprintf("%s %d", pre, n))
				}
			}
			fmt.Printf("  (%s)", strings.Join(parts, ", "))
		}
		fmt.Println()
		for _, s := range problems {
			fmt.Println("  !", s)
		}

		// Session log: today's entries, newest last.
		entries, err := session.Today(p)
		if err != nil {
			return err
		}
		fmt.Printf("\nsession   %d entr%s today\n", len(entries), plural(len(entries), "y", "ies"))
		for _, e := range entries {
			fmt.Printf("  %s  %-9s %-8s %s\n", e.At.Format("15:04"), e.Agent, e.Kind, e.Text)
		}
		if len(entries) == 0 {
			fmt.Println("  log the first one: gears session new --kind done \"...\"")
		}
		return nil
	},
}

func init() {
	statusCmd.Flags().StringVarP(&statusAgent, "agent", "a", "", "view as this agent (default: $GEARS_AGENT or \"local\")")
	root.AddCommand(statusCmd)
}

func mustCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return dir
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
