package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/project"
)

var (
	initSlug   string
	initPrefix string
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Bind this directory to a project",
	Long: `Bind the current directory to a project.

Writes .gears/project.yaml, creates .gears/artifacts/, adds the local cache
to .gitignore, and writes a short Gears block into AGENTS.md. No skeleton
tree. Safe to run again: existing files are left alone.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		res, err := project.Init(dir, initSlug, initPrefix)
		if err != nil {
			return err
		}
		for _, line := range res.Actions {
			fmt.Println(" ", line)
		}
		fmt.Printf("\nBound %s to project %q.\nNext: gears status\n", dir, res.Project.Slug)
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&initSlug, "slug", "", "project slug (default: directory name)")
	initCmd.Flags().StringVar(&initPrefix, "prefix", "", "card key prefix, e.g. GRS (default: from the slug)")
	root.AddCommand(initCmd)
}
