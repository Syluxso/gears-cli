package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	artifactTagFilter string
	artifactProject   string
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
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.ArtifactNew(c, verbs.ArtifactNewIn{Prefix: args[0], Title: strings.Join(args[1:], " "), Project: artifactProject})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("%s  %s\n", tail(out.ID), out.Path)
		return nil
	},
}

var artifactListCmd = &cobra.Command{
	Use:   "list",
	Short: "List artifacts with tags and shape status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.ArtifactList(c, verbs.ArtifactListIn{Tag: artifactTagFilter})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Artifacts) == 0 {
			fmt.Println("no artifacts. create one: gears artifact new adr \"...\"")
			return nil
		}
		for _, a := range out.Artifacts {
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
			fmt.Printf("%s  %-7s %-48s %s%s\n", tail(a.ID), a.Prefix, a.File, mark, tags)
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
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.ArtifactTagIn{Artifact: args[0]}
		for _, t := range args[1:] {
			if strings.HasPrefix(t, "-") {
				in.Remove = append(in.Remove, t[1:])
			} else {
				in.Add = append(in.Add, strings.TrimPrefix(t, "+"))
			}
		}
		out, err := verbs.ArtifactTag(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("%s  #%s\n", out.File, strings.Join(out.Tags, " #"))
		return nil
	},
}

var artifactDeleteCmd = &cobra.Command{
	Use:   "delete <file|id|short id>",
	Short: "Delete an artifact: row and file together",
	Long: `Removes the row and the file in one step, so reconcile neither
regenerates the file nor re-adopts it. Works offline: the delete is kept
until the next gears sync soft-deletes it on the server, and other machines
drop the row and the file when they sync.

  gears artifact delete free--agent-handoff.md
  gears artifact delete 3f2a9c1d`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.ArtifactDelete(c, verbs.ArtifactDeleteIn{Artifact: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("deleted %s  %s (row and file; pushed on next gears sync)\n", tail(out.ID), out.File)
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
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.ArtifactReconcile(c, verbs.ArtifactReconcileIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Changes) == 0 {
			fmt.Println("rows and files agree")
			return nil
		}
		for _, ch := range out.Changes {
			fmt.Printf("%-11s %-48s %s\n", ch.Action, ch.File, ch.Detail)
		}
		return nil
	},
}

var artifactValidateCmd = &cobra.Command{
	Use:   "validate [file]",
	Short: "Check artifacts against their shapes",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.ArtifactValidateIn{}
		if len(args) == 1 {
			in.File = args[0]
		}
		out, err := verbs.ArtifactValidate(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			if len(out.Problems) > 0 {
				return fmt.Errorf("%d artifact%s failed shape validation", len(out.Problems), plural(len(out.Problems), "", "s"))
			}
			return nil
		}
		for _, p := range out.Problems {
			fmt.Println(p.File)
			for _, e := range p.Errors {
				fmt.Printf("  - %s\n", e)
			}
		}
		if len(out.Problems) > 0 {
			return fmt.Errorf("%d artifact%s failed shape validation", len(out.Problems), plural(len(out.Problems), "", "s"))
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
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.Shapes(c, verbs.ShapesIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		for _, s := range out.Shapes {
			fmt.Printf("%-7s %s  [%s]\n", s.Prefix, s.Title, s.Source)
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
	artifactListCmd.Flags().StringVar(&artifactTagFilter, "tag", "", "only artifacts with this tag")
	artifactNewCmd.Flags().StringVar(&artifactProject, "project", "", "the project it belongs to: slug, id or path (default: the project the directory is in)")
	artifactCmd.AddCommand(artifactNewCmd, artifactListCmd, artifactTagCmd, artifactDeleteCmd, artifactReconcileCmd, artifactValidateCmd, artifactShapesCmd)
	root.AddCommand(artifactCmd)
}
