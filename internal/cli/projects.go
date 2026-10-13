package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

var projectsOffline bool

var projectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "The workspace's projects (.gears/projects.yaml)",
	Long: `The git repos under the workspace root's projects/ directory, recorded in
.gears/projects.yaml as {id, slug, path, remote}. The manifest is a cache of
gears-api's project list; gears sync makes every non-missing entry exist
there. Commands run inside projects/<repo> tag session entries and new
artifacts with that project's id.`,
}

var projectsListCmd = &cobra.Command{
	Use:   "list",
	Short: "Print the manifest",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.ProjectsList(c, verbs.ProjectsListIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Projects) == 0 {
			fmt.Printf("no projects in %s; run gears projects scan\n", out.Root)
			return nil
		}
		for _, e := range out.Projects {
			mark := " "
			if e.ID == out.Current {
				mark = "*"
			}
			state := ""
			if e.Missing {
				state = "  MISSING"
			}
			remote := e.Remote
			if remote == "" {
				remote = "(no remote)"
			}
			fmt.Printf("%s %s  %-14s %-22s %s%s\n", mark, e.ID, e.Slug, e.Path, remote, state)
		}
		return nil
	},
}

var projectsScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Find git repos under projects/ and record them",
	Long: `Scan <workspace root>/projects/*/ for git repos (any directory with .git,
with or without commits or a remote). New repos are added: when logged in, a
repo whose remote gears-api already knows in this workspace takes that
project's id; otherwise a fresh UUID v7, which sync reconciles. Repos that
are gone are marked missing, never dropped. Runs from anywhere in the
workspace.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.ProjectsScan(c, verbs.ProjectsScanIn{Offline: projectsOffline})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("%s: %d project%s\n", out.Root, len(out.Projects), plural(len(out.Projects), "", "s"))
		printScan(out)
		return nil
	},
}

func printScan(out *verbs.ProjectsScanOut) {
	for _, s := range out.Added {
		fmt.Println("  added   ", s)
	}
	for _, s := range out.Returned {
		fmt.Println("  back    ", s)
	}
	for _, s := range out.Updated {
		fmt.Println("  remote  ", s)
	}
	for _, s := range out.Adopted {
		fmt.Println("  adopted ", s)
	}
	for _, s := range out.Missing {
		fmt.Println("  missing ", s)
	}
	for _, n := range out.Notes {
		fmt.Println("  note:", n)
	}
}

func init() {
	projectsScanCmd.Flags().BoolVar(&projectsOffline, "offline", false, "do not ask gears-api for known remotes")
	projectsCmd.AddCommand(projectsListCmd, projectsScanCmd)
	root.AddCommand(projectsCmd)
}
