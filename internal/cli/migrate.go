package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/migrate"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	migrateDryRun  bool
	migrateOffline bool
)

var migrateCmd = &cobra.Command{
	Use:   "migrate [dir]",
	Short: "Upgrade a gears v2 workspace (old .gears) to v3",
	Long: `Upgrade the gears v2 workspace at dir (default: the current directory).

  1. renames .gears to .gears_v2 (kept, so you can restore it)
  2. writes .gears_v2.zip, an archive of that tree
  3. runs gears init there (and gears projects scan when projects/ exists)
  4. copies each v2 markdown file (artifacts, ADRs, decisions, stories,
     sessions, context/memory/instructions notes) into .gears/artifacts/ as
     free--migrated--<kind>-<name>.md with a title in its front matter

Everything is a free artifact, so v3 shape checks never apply; the v2 kind
starts the name: adr (adr--*, adr_*, decisions/*), story (story--*, story/*),
idea (idea--*), index (every index.md), nothing for other notes. The v2 database
(.gearbox/gears.db), its config and hydrate file, v2's own README and
gears-init.md, adr_template.md, the shipped adr_example-* files and
hydrate reports are not migrated; they stay in .gears_v2.

Runs only at a workspace root: refused inside a v3 workspace, where there
is no v2 .gears, or where a v3 workspace has no .gears_v2. Safe to re-run:
artifacts already migrated are kept and counted. --dry-run prints the plan
and changes nothing.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := mustCwd()
		if len(args) == 1 {
			dir = args[0]
		}
		plan, err := migrate.Detect(dir)
		if err != nil {
			return err
		}
		if migrateDryRun {
			if emit(plan) {
				return nil
			}
			fmt.Printf("Dry run: migrate %s (nothing changed)\n", plan.Dir)
			printMigratePlan(plan)
			return nil
		}
		res, err := migrate.Run(plan)
		if err != nil {
			return err
		}
		p := res.Project
		var scan *verbs.ProjectsScanOut
		if dirExists(filepath.Join(plan.Dir, project.ProjectsDir)) || p.HasManifest() {
			scan, err = verbs.ProjectsScan(verbs.Ctx{Project: p, Agent: globalAgent}, verbs.ProjectsScanIn{Offline: migrateOffline})
			if err != nil {
				return err
			}
		}
		if emit(map[string]any{"plan": plan, "init": res.Init, "scan": scan}) {
			return nil
		}
		fmt.Printf("Migrated %s\n", plan.Dir)
		if plan.Rename {
			fmt.Println("  renamed .gears -> " + migrate.OldDir)
		}
		if plan.Zip {
			fmt.Println("  wrote   " + migrate.ZipFile)
		}
		for _, l := range res.Init {
			fmt.Println(" ", l)
		}
		if scan != nil {
			fmt.Printf("  scanned %s/: %d project%s\n", project.ProjectsDir, len(scan.Projects), plural(len(scan.Projects), "", "s"))
			printScan(scan)
		}
		printMigrateItems(plan, true)
		fmt.Printf(`
Migration done. %[1]s/ is kept: to go back, delete .gears and rename
%[1]s to .gears. %[2]s is an archive of it: store it or delete it.
Next: gears login, then gears sync.
`, migrate.OldDir, migrate.ZipFile)
		return nil
	},
}

func printMigratePlan(plan *migrate.Plan) {
	if plan.Rename {
		fmt.Println("  rename  .gears -> " + migrate.OldDir)
	}
	if plan.Zip {
		fmt.Println("  zip     " + migrate.OldDir + " -> " + migrate.ZipFile)
	}
	if plan.Init {
		fmt.Println("  init    v3 workspace (.gears/project.yaml)")
	}
	if dirExists(filepath.Join(plan.Dir, project.ProjectsDir)) {
		fmt.Println("  scan    projects/")
	}
	printMigrateItems(plan, false)
}

func printMigrateItems(plan *migrate.Plan, done bool) {
	verb, count := "create ", "to create"
	if done {
		verb, count = "created", "created"
	}
	for _, it := range plan.Items {
		switch it.Action {
		case "create":
			fmt.Printf("  %s %s  <- %s/%s\n", verb, it.File, migrate.OldDir, it.Source)
		case "exists":
			fmt.Printf("  kept    %s  (already migrated)\n", it.File)
		default:
			fmt.Printf("  skip    %s/%s  (%s)\n", migrate.OldDir, it.Source, it.Reason)
		}
	}
	c, e, s := plan.Counts()
	fmt.Printf("  artifacts: %d %s, %d already migrated, %d skipped\n", c, count, e, s)
}

func init() {
	migrateCmd.Flags().BoolVar(&migrateDryRun, "dry-run", false, "print the plan and change nothing")
	migrateCmd.Flags().BoolVar(&migrateOffline, "offline", false, "scan projects/ without asking gears-api for known remotes")
	root.AddCommand(migrateCmd)
}
