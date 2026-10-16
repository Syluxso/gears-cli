package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// Official runbooks (GRS-59): adopt, edit, unadopt, through gears-api;
// gears runbook list shows the catalog.

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

func printDetail(d *api.RunbookDetail) {
	if d.Note != "" {
		fmt.Println("(" + d.Note + ")")
	}
	fmt.Printf("%s  %s  (%s, %s", d.Runbook, d.Title, d.Source, frozenWord(d.Frozen))
	if d.SourceRef != "" && d.SourceRef != d.Runbook {
		fmt.Printf(", from %s v%d", d.SourceRef, d.SourceVersion)
	}
	fmt.Println(")")
	if len(d.Adopted) > 0 {
		fmt.Println("  adopted as " + strings.Join(d.Adopted, ", "))
	}
	for i, s := range d.Steps {
		title := s.Title
		if title == "" {
			title = s.ID
		}
		line := fmt.Sprintf("  %02d  %-12s %-12s %s", i+1, s.Kind, s.ID, title)
		if s.Unknown {
			line += "  (unknown kind)"
		}
		if len(s.EvidenceKinds) > 0 {
			line += "  proof: " + strings.Join(s.EvidenceKinds, "|")
		}
		for k, v := range s.Params {
			line += fmt.Sprintf("  %s=%v", k, v)
		}
		fmt.Println(line)
	}
}

var runbookAbilitiesCmd = &cobra.Command{
	Use:   "abilities",
	Short: "The step-kind catalog: kinds, their tool, params and the evidence that proves them",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookAbilities(c, verbs.RunbookAbilitiesIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		for _, a := range out.Items {
			var ps []string
			for _, p := range a.Params {
				n := p.Name
				if p.Required {
					n += "*"
				}
				ps = append(ps, n)
			}
			fmt.Println(fmt.Sprintf("%-14s %-22s proof %-30s params %s", a.Kind, a.Tool, strings.Join(a.EvidenceKinds, "|"), strings.Join(ps, ", ")))
		}
		return nil
	},
}

var runbookAdoptCmd = &cobra.Command{
	Use:   "adopt <ref> [--as <name>]",
	Short: "Adopt an official runbook into this workspace",
	Long: `Copies an official runbook (gears runbook list) into the workspace on
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
	runbookCmd.AddCommand(runbookAdoptCmd, runbookEditCmd, runbookAbilitiesCmd, runbookUnadoptCmd)
}

var runbookUnadoptCmd = &cobra.Command{
	Use:   "unadopt <slug>",
	Short: "Remove an adopted runbook (frozen or editable) from the workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookUnadopt(c, verbs.RunbookUnadoptIn{Runbook: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Println("unadopted " + out.Runbook)
		return nil
	},
}
