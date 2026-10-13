package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	initSlug    string
	initPrefix  string
	initOffline bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Bind this directory to a workspace (or a standalone clone)",
	Long: `Bind the current directory.

Writes .gears/project.yaml, creates .gears/artifacts/, adds .gears/ to
.gitignore, and writes a short Gears block into AGENTS.md. No skeleton
tree. Safe to run again: existing files are left alone.

At a workspace root (a directory with projects/), init also scans
projects/*/ for git repos and records them in .gears/projects.yaml, the same
as gears projects scan. The workspace root is the only .gears: init inside a
directory that is already under a binding is refused. A standalone clone
with no workspace above it still binds on its own, by git remote, at the
next gears login or sync.`,
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
		p := res.Project
		if p.IsWorkspace() && (dirExists(filepath.Join(dir, project.ProjectsDir)) || p.HasManifest()) {
			agent := globalAgent
			out, err := verbs.ProjectsScan(verbs.Ctx{Project: p, Agent: agent}, verbs.ProjectsScanIn{Offline: initOffline})
			if err != nil {
				return err
			}
			fmt.Printf("  scanned %s/: %d project%s in .gears/%s\n", project.ProjectsDir, len(out.Projects), plural(len(out.Projects), "", "s"), project.ManifestFile)
			printScan(out)
		}
		fmt.Printf("\nBound %s to %q.\nNext: gears status\n", dir, p.Slug)
		return nil
	},
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func init() {
	initCmd.Flags().StringVar(&initSlug, "slug", "", "slug (default: directory name)")
	initCmd.Flags().StringVar(&initPrefix, "prefix", "", "card key prefix, e.g. GRS (default: from the slug)")
	initCmd.Flags().BoolVar(&initOffline, "offline", false, "scan without asking gears-api for known remotes")
	root.AddCommand(initCmd)
}
