package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// Official runbooks (GRS-59): catalog, adopt, edit, through gears-api.

var (
	rbDescription string
	rbStepsFile   string
)

func frozenWord(f bool) string {
	if f {
		return "frozen"
	}
	return "editable"
}

func printAdopted(rb *api.AdoptedRunbook) {
	fmt.Printf("%s  %s  (%s, from %s v%d)\n", rb.Slug, rb.Title, frozenWord(rb.Frozen), rb.SourceRef, rb.SourceVersion)
	for i, s := range rb.Steps {
		title := s.Title
		if title == "" {
			title = s.ID
		}
		fmt.Printf("  %02d  %-12s %-12s %s\n", i+1, s.Kind, s.ID, title)
	}
}

var runbookCatalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Official runbooks (frozen or editable) and this workspace's adopted copies",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookCatalog(c, verbs.RunbookCatalogIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Println("official")
		for _, o := range out.Official {
			line := fmt.Sprintf("  %-28s %-9s %d steps  %s", o.Ref, frozenWord(o.Frozen), len(o.Steps), o.Title)
			if len(o.Adopted) > 0 {
				line += fmt.Sprintf("  (adopted as %v)", o.Adopted)
			}
			fmt.Println(line)
		}
		fmt.Println("adopted")
		if len(out.Adopted) == 0 {
			fmt.Println("  none: gears runbook adopt <ref>")
		}
		for _, rb := range out.Adopted {
			fmt.Printf("  %-28s %-9s %d steps  from %s\n", rb.Slug, frozenWord(rb.Frozen), len(rb.Steps), rb.SourceRef)
		}
		return nil
	},
}

var runbookAdoptCmd = &cobra.Command{
	Use:   "adopt <ref> [--as <name>]",
	Short: "Adopt an official runbook into this workspace",
	Long: `Copies an official runbook (gears runbook catalog) into the workspace on
gears-api. A frozen one becomes a read-only copy whose runs always use the
official steps; an editable one becomes the workspace's own copy (default
slug <ref>-copy) that gears runbook edit changes. Start either with
gears runbook start <slug>.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookAdopt(c, verbs.RunbookAdoptIn{Ref: args[0], As: rbAs})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printAdopted(out)
		return nil
	},
}

var runbookEditCmd = &cobra.Command{
	Use:   "edit <slug> [--title ...] [--description ...] [--steps-file steps.json]",
	Short: "Change an adopted editable runbook (a frozen copy is refused)",
	Long: `--steps-file is a JSON array of steps, each {"id", "kind", "title", "params"}
on the orchestrator's ability catalog; it replaces the whole list.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.RunbookEditIn{Runbook: args[0]}
		if cmd.Flags().Changed("title") {
			in.Title = &rbTitle
		}
		if cmd.Flags().Changed("description") {
			in.Description = &rbDescription
		}
		if rbStepsFile != "" {
			b, err := os.ReadFile(rbStepsFile)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(b, &in.Steps); err != nil {
				return fmt.Errorf("%s: %w", rbStepsFile, err)
			}
		}
		out, err := verbs.RunbookEdit(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printAdopted(out)
		return nil
	},
}

func init() {
	runbookAdoptCmd.Flags().StringVar(&rbAs, "as", "", "name for the copy (its slug is derived)")
	runbookEditCmd.Flags().StringVar(&rbTitle, "title", "", "new title")
	runbookEditCmd.Flags().StringVar(&rbDescription, "description", "", "new description")
	runbookEditCmd.Flags().StringVar(&rbStepsFile, "steps-file", "", "JSON file with the whole new step list")
	runbookCmd.AddCommand(runbookCatalogCmd, runbookAdoptCmd, runbookEditCmd)
}
