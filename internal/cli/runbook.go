package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/verbs"
)

var (
	rbKind   string
	rbCheck  string
	rbTarget string
	rbRun    string
	rbReason string
)

var runbookCmd = &cobra.Command{
	Use:   "runbook",
	Short: "Processes an agent cannot fake",
	Long: `A runbook is an ordered list of steps for one kind of work, kept as
runbook--<slug>.yaml in .gears/artifacts/ and authored only here. Starting
one creates a run; each step must leave evidence, and evidence that names
a branch, commit, session entry, desk item or artifact is verified against
the real thing before the step counts.

Step kinds and the checks they may declare:
  ask    prompt the user                    (no check)
  do     agent work                         commit
  gears  a Gears verb run as the step       session | desk | artifact
  git    a repository state                 branch | commit | pushed
  gate   wait for a condition               (no check)`,
}

var runbookNewCmd = &cobra.Command{
	Use:   "new <title>",
	Short: "Create an empty runbook",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookNew(c, verbs.RunbookNewIn{Title: strings.Join(args, " ")})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("%s\nadd steps: gears runbook step add %s --kind ask \"...\"\n", out.Path, out.Slug)
		return nil
	},
}

var runbookStepCmd = &cobra.Command{Use: "step", Short: "Author or complete steps"}

var runbookStepAddCmd = &cobra.Command{
	Use:   "add <slug> <text>",
	Short: "Append a step to a runbook",
	Long: `  gears runbook step add single-ticket --kind ask "Which ticket?"
  gears runbook step add single-ticket --kind git --check branch "Branch from main named after the ticket"
  gears runbook step add single-ticket --kind gears --check session "Log a session entry"`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookStepAdd(c, verbs.RunbookStepAddIn{Runbook: args[0], Kind: rbKind, Text: strings.Join(args[1:], " "), Check: rbCheck})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("%s: %d step%s\n", out.Slug, len(out.Steps), plural(len(out.Steps), "", "s"))
		return nil
	},
}

var runbookListCmd = &cobra.Command{
	Use:   "list",
	Short: "Runbooks in this project, and active runs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookList(c, verbs.RunbookListIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Runbooks) == 0 {
			fmt.Println("no runbooks. create one: gears runbook new \"Single ticket development\"")
		}
		for _, rb := range out.Runbooks {
			fmt.Printf("%-32s %-40s %d step%s\n", rb.Slug, rb.Title, len(rb.Steps), plural(len(rb.Steps), "", "s"))
		}
		if len(out.Active) > 0 {
			fmt.Println("\nactive runs")
			for _, r := range out.Active {
				printRunSummary(r)
			}
		}
		return nil
	},
}

func printRunSummary(r verbs.RunSummary) {
	cur := "—"
	if r.Current != nil {
		cur = fmt.Sprintf("step %d: %s", r.Current.Idx, r.Current.Text)
	}
	target := r.Target
	if target != "" {
		target = " " + target
	}
	fmt.Printf("%s  %s%s  %s  %d/%d  %s\n", short(r.ID), r.Runbook, target, r.Agent, r.Done, r.Total, cur)
}

func printRun(r *verbs.Run) {
	fmt.Printf("run %s  %s", short(r.ID), r.Runbook)
	if r.Target != "" {
		fmt.Printf("  target %s", r.Target)
	}
	fmt.Printf("  agent %s  %s\n", r.Agent, r.Status)
	marks := map[string]string{"pending": "·", "current": "▶", "done": "✓", "verified": "✓✓", "skipped": "↷"}
	for _, s := range r.Steps {
		line := fmt.Sprintf("  %-2s %02d  %-6s %s", marks[s.State], s.Idx, s.Kind, s.Text)
		if s.Evidence != "" {
			line += "  [" + s.Evidence + "]"
		}
		if s.Reason != "" {
			line += "  (" + s.Reason + ")"
		}
		if s.DoneAt != "" {
			line += "  " + hhmm(s.DoneAt)
		}
		fmt.Println(line)
	}
}

var runbookShowCmd = &cobra.Command{
	Use:   "show <slug|run-id>",
	Short: "Show a runbook's steps, or a run's progress",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookShow(c, verbs.RunbookShowIn{Key: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.Run != nil {
			printRun(out.Run)
			return nil
		}
		fmt.Printf("%s  (%s)\n", out.Runbook.Title, out.Runbook.Slug)
		for _, s := range out.Runbook.Steps {
			check := ""
			if s.Check != "" {
				check = "  evidence: " + s.Check
			}
			fmt.Printf("  %02d  %-6s %s%s\n", s.Idx, s.Kind, s.Text, check)
		}
		return nil
	},
}

var runbookStartCmd = &cobra.Command{
	Use:   "start <slug>",
	Short: "Start a run; its first step lands on the desk",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookStart(c, verbs.RunbookStartIn{Runbook: args[0], Target: rbTarget})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printRun(out)
		return nil
	},
}

func printStepOut(out *verbs.RunStepOut) {
	state := "done"
	if out.Verified {
		state = "verified"
	}
	if out.Step.State == "skipped" {
		fmt.Printf("step %d skipped: %s\n", out.Step.Idx, out.Step.Reason)
	} else {
		fmt.Printf("step %d %s (%s)\n", out.Step.Idx, state, out.Note)
	}
	if out.Finished {
		fmt.Printf("run %s completed\n", short(out.RunID))
	} else if out.Next != nil {
		fmt.Printf("next: step %d  %-6s %s\n", out.Next.Idx, out.Next.Kind, out.Next.Text)
	}
}

var runbookStepDoneCmd = &cobra.Command{
	Use:   "done <evidence>",
	Short: "Complete the current step with evidence",
	Long: `Evidence is what the step's check asks for: a branch name, a commit,
a session entry id, a desk item id, an artifact file, or a note for steps
without a check. Verification failure leaves the step current.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunStepDone(c, verbs.RunStepDoneIn{RunRef: verbs.RunRef{Run: rbRun}, Evidence: strings.Join(args, " ")})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printStepOut(out)
		return nil
	},
}

var runbookSkipCmd = &cobra.Command{
	Use:   "skip",
	Short: "Skip the current step, with a reason",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunSkip(c, verbs.RunSkipIn{RunRef: verbs.RunRef{Run: rbRun}, Reason: rbReason})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printStepOut(out)
		return nil
	},
}

var runbookAbandonCmd = &cobra.Command{
	Use:   "abandon",
	Short: "End a run without completing it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunAbandon(c, verbs.RunAbandonIn{RunRef: verbs.RunRef{Run: rbRun}, Reason: rbReason})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("run %s abandoned\n", short(out.ID))
		return nil
	},
}

func init() {
	runbookStepAddCmd.Flags().StringVarP(&rbKind, "kind", "k", "do", "ask | do | gears | git | gate")
	runbookStepAddCmd.Flags().StringVar(&rbCheck, "check", "", "evidence check for the kind (see runbook --help)")
	runbookStartCmd.Flags().StringVar(&rbTarget, "target", "", "what the run is about: a card key, a repo, a slug")
	for _, c := range []*cobra.Command{runbookStepDoneCmd, runbookSkipCmd, runbookAbandonCmd} {
		c.Flags().StringVar(&rbRun, "run", "", "run id (default: the only active run)")
	}
	runbookSkipCmd.Flags().StringVar(&rbReason, "reason", "", "why the step is skipped (required)")
	runbookAbandonCmd.Flags().StringVar(&rbReason, "reason", "", "why the run is abandoned (required)")

	runbookStepCmd.AddCommand(runbookStepAddCmd, runbookStepDoneCmd)
	runbookCmd.AddCommand(runbookNewCmd, runbookListCmd, runbookShowCmd, runbookStartCmd, runbookStepCmd, runbookSkipCmd, runbookAbandonCmd)
	root.AddCommand(runbookCmd)
}
