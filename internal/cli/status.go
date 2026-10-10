package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/artifact"
	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/session"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Cold-start packet for this project",
	Long: `Everything an agent needs before it starts: the bound project, the
artifacts and whether they pass their shapes, and the latest session entries.
Run it first in every session.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}

		fmt.Printf("project   %s\n", p.Slug)
		fmt.Printf("root      %s\n", p.Root)

		// Artifacts, grouped by prefix, with validation.
		list, err := artifact.List(p)
		if err != nil {
			return err
		}
		counts := map[string]int{}
		var problems []string
		for _, a := range list {
			counts[a.Prefix]++
			if errs := artifact.Validate(p, a); len(errs) > 0 {
				problems = append(problems, fmt.Sprintf("%s: %s", a.File, strings.Join(errs, "; ")))
			}
		}
		fmt.Printf("artifacts %d", len(list))
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
		if len(problems) > 0 {
			fmt.Println("\nshape problems")
			for _, s := range problems {
				fmt.Println("  !", s)
			}
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
