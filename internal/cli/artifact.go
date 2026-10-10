package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/artifact"
	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/shape"
)

var (
	artifactAgent string
	artifactTag   string
)

var artifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "Shaped files beside the code: adr, story, idea, index, free",
	Long: `Artifacts are prefixed files in .gears/artifacts/ ruled by a row in the
local cache. The row is created first and holds identity, tags, hash and
shape result; the file holds the body. The two are reconciled on every
list and status.`,
}

var artifactNewCmd = &cobra.Command{
	Use:   "new <prefix> <title>",
	Short: "Create an artifact from its shape",
	Long: `Create a new artifact: the row first, then the file seeded with the
required front matter and section headings, so an agent fills blanks
instead of inventing structure.

  gears artifact new adr "Clean Gin build on byz-platform"
  gears artifact new story "Desk handoff"`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		a, err := artifact.New(p, args[0], strings.Join(args[1:], " "), artifactAgent)
		if err != nil {
			return err
		}
		fmt.Printf("%s  %s\n", a.ID[:8], a.Path)
		return nil
	},
}

var artifactListCmd = &cobra.Command{
	Use:   "list",
	Short: "List artifacts with tags and shape status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		list, err := artifact.List(p)
		if err != nil {
			return err
		}
		n := 0
		for _, a := range list {
			if artifactTag != "" && !has(a.Tags, project.Slugify(artifactTag)) {
				continue
			}
			n++
			mark := "ok"
			if !a.FilePresent {
				mark = "!! file missing"
			} else if !a.ShapeOK {
				mark = "!! " + strings.Join(a.ShapeErrors, "; ")
			}
			tags := ""
			if len(a.Tags) > 0 {
				tags = " #" + strings.Join(a.Tags, " #")
			}
			fmt.Printf("%s  %-7s %-48s %s%s\n", a.ID[:8], a.Prefix, a.File, mark, tags)
		}
		if n == 0 {
			fmt.Println("no artifacts. create one: gears artifact new adr \"...\"")
		}
		return nil
	},
}

var artifactTagCmd = &cobra.Command{
	Use:   "tag <file|id> <+tag|-tag>...",
	Short: "Add or remove tags on an artifact",
	Long: `Tags live on the row, never in the file.

  gears artifact tag adr--001-clean-gin-build.md auth api
  gears artifact tag 3f2a9c1 -api`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		a, err := artifact.Tag(p, args[0], args[1:])
		if err != nil {
			return err
		}
		fmt.Printf("%s  #%s\n", a.File, strings.Join(a.Tags, " #"))
		return nil
	},
}

var artifactReconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Make rows and files agree",
	Long: `Regenerate files that have a row but no file, adopt files that have no
row, and update rows whose file changed. Runs automatically on list and
status; call it directly to see what it did.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		changes, err := artifact.Reconcile(p)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			fmt.Println("rows and files agree")
			return nil
		}
		for _, c := range changes {
			fmt.Printf("%-11s %-48s %s\n", c.Action, c.File, c.Detail)
		}
		return nil
	},
}

var artifactValidateCmd = &cobra.Command{
	Use:   "validate [file]",
	Short: "Check artifacts against their shapes",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		list, err := artifact.List(p)
		if err != nil {
			return err
		}
		bad := 0
		for _, a := range list {
			if len(args) == 1 && a.File != args[0] {
				continue
			}
			if errs := artifact.Validate(p, a); len(errs) > 0 {
				bad++
				fmt.Printf("%s\n", a.File)
				for _, e := range errs {
					fmt.Printf("  - %s\n", e)
				}
			}
		}
		if bad > 0 {
			return fmt.Errorf("%d artifact%s failed shape validation", bad, plural(bad, "", "s"))
		}
		fmt.Println("all artifacts pass their shapes")
		return nil
	},
}

var artifactShapesCmd = &cobra.Command{
	Use:   "shapes",
	Short: "Show the shapes in force for this project",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		set, err := shape.Load(p.ArtifactsDir())
		if err != nil {
			return err
		}
		for _, pre := range artifact.Prefixes {
			s, ok := set[pre]
			if !ok {
				continue
			}
			src := "built-in"
			if s.Source != "" {
				src = s.Source
			}
			fmt.Printf("%-7s %s  [%s]\n", pre, s.Title, src)
			if len(s.FrontMatter) > 0 {
				fmt.Printf("        front matter: %s\n", strings.Join(s.FrontMatter, ", "))
			}
			if len(s.Sections) > 0 {
				fmt.Printf("        sections:     %s\n", strings.Join(s.Sections, " / "))
			}
			if s.MinBody > 0 {
				fmt.Printf("        min body:     %d chars\n", s.MinBody)
			}
		}
		return nil
	},
}

func init() {
	artifactNewCmd.Flags().StringVarP(&artifactAgent, "agent", "a", "", "agent label recorded as creator (default: $GEARS_AGENT or \"local\")")
	artifactListCmd.Flags().StringVar(&artifactTag, "tag", "", "only artifacts with this tag")
	artifactCmd.AddCommand(artifactNewCmd, artifactListCmd, artifactTagCmd, artifactReconcileCmd, artifactValidateCmd, artifactShapesCmd)
	root.AddCommand(artifactCmd)
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
