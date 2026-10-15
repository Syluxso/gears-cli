package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	initSlug    string
	initPrefix  string
	initOffline bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a Gears workspace in this directory",
	Long: `Create a Gears workspace in the current directory.

Writes .gears/project.yaml, creates .gears/artifacts/, adds .gears/ to
.gitignore, and writes a short Gears block into AGENTS.md. No skeleton
tree. At a workspace root it also writes a stub architecture doc,
.gears/artifacts/arch--primary.md (the master design doc gears status
always shows in full). Safe to run again at the root: existing files,
the architecture doc included, are left alone.

At a workspace root (a directory with projects/), init also scans
projects/*/ for git repos and records them in .gears/projects.yaml, the same
as gears projects scan.

Workspace rules:
  - One .gears per workspace, at its root. Run init in a directory with no
    .gears above it, and that directory becomes the workspace root.
  - init anywhere inside an existing workspace (any .gears above) is
    refused and names the root. No .gears is ever created below it.
  - Every other command run anywhere under the workspace walks up to the
    root's .gears and works there; outside any workspace it says so.
  - A nested .gears left over from an older version or another machine is
    ignored: the outermost one wins, and commands print one warning naming
    the stray so you can delete it.`,
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
		if p.IsWorkspace() {
			// The workspace's architecture doc (GRS-41): a stub once, never
			// overwritten on a re-run.
			agent, _ := session.ResolveAgent(globalAgent)
			a, created, err := artifact.InitArch(p, agent)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, a.Path)
			if created {
				fmt.Println("  wrote  ", filepath.ToSlash(rel), "(architecture stub: fill it in; gears status always shows it)")
			} else {
				fmt.Println("  kept   ", filepath.ToSlash(rel))
			}
		}
		if p.IsWorkspace() &&(dirExists(filepath.Join(dir, project.ProjectsDir)) || p.HasManifest()) {
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
