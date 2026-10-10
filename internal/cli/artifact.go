package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/artifact"
	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/shape"
)

var artifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "Shaped files beside the code: adr, story, idea, index, free",
}

var artifactNewCmd = &cobra.Command{
	Use:   "new <prefix> <title>",
	Short: "Create an artifact from its shape",
	Long: `Create a new artifact file in .gears/artifacts/ from the shape for its
prefix. The file is named <prefix>--<slug>.md and seeded with the required
front matter and section headings, so an agent fills blanks instead of
inventing structure.

  gears artifact new adr "Clean Gin build on byz-platform"
  gears artifact new story "Desk handoff"`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		title := strings.Join(args[1:], " ")
		a, err := artifact.New(p, args[0], title)
		if err != nil {
			return err
		}
		fmt.Println(a.Path)
		return nil
	},
}

var artifactListCmd = &cobra.Command{
	Use:   "list",
	Short: "List artifacts with their shape status",
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
		if len(list) == 0 {
			fmt.Println("no artifacts. create one: gears artifact new adr \"...\"")
			return nil
		}
		for _, a := range list {
			mark := "ok"
			if errs := artifact.Validate(p, a); len(errs) > 0 {
				mark = "!! " + strings.Join(errs, "; ")
			}
			fmt.Printf("%-7s %-48s %s\n", a.Prefix, a.File, mark)
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
	artifactCmd.AddCommand(artifactNewCmd, artifactListCmd, artifactValidateCmd, artifactShapesCmd)
	root.AddCommand(artifactCmd)
}
