package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Cold-start packet for this project",
	Long: `Everything an agent needs before it starts: the bound project, open desk
items, active runs, artifacts and whether they pass their shapes, and the
latest session entries. Run it first in every session.`,
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

		fmt.Printf("project   %s\n", out.Project)
		fmt.Printf("agent     %s\n", out.Agent)
		fmt.Printf("root      %s\n", out.Root)

		fmt.Printf("\ndesk      %d open item%s\n", len(out.Desk), plural(len(out.Desk), "", "s"))
		for _, it := range out.Desk {
			fmt.Printf("  %s  %-8s %s → %s  %s\n", short(it.ID), it.Type, it.From, orAnyone(it.To), it.Text)
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
		return nil
	},
}

func init() {
	root.AddCommand(statusCmd)
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
