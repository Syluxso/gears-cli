package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	rbKind       string
	rbCheck      string
	rbName       string
	rbWhen       string
	rbTargetKind string
	rbTarget     string
	rbRun        string
	rbReason     string
)

var runbookCmd = &cobra.Command{
	Use:   "runbook",
	Short: "Processes an agent cannot fake",
	Long: `A runbook is an ordered list of steps for one kind of work, kept as
runbook--<slug>.yaml in .gears/artifacts/ and authored only here. Starting
one creates a run bound to a card and, once a branch step passes, a branch.
Each step is performed or verified against the run, not merely recorded.

Rules: one active run per agent; only the run's agent advances it (takeover
with a reason moves it); a run owns its card's state; evidence is a Gears
or git object, never a shell command.

Step kinds and the checks they may declare (check or check:arg):
  ask    prompt the user; the answer is the evidence   (no check; --name it for when:)
  do     agent work                                    commit | clean
  gears  a Gears verb performed as the step            session | desk | artifact | card:<State>
  git    a repository state                            branch | commit | pushed | merged:<base> | clean
  gate   wait until a check holds                      pushed | merged:<base> | card:<State> | desk`,
}

var runbookNewCmd = &cobra.Command{
	Use:   "new <title>",
	Short: "Create an empty runbook (--target card|repo|none)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookNew(c, verbs.RunbookNewIn{Title: strings.Join(args, " "), Target: rbTargetKind})
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
	Long: `  gears runbook step add single-ticket --kind gears --check "card:In Progress" "Take the card"
  gears runbook step add single-ticket --kind git --check branch "Branch from main, named after the card"
  gears runbook step add single-ticket --kind do --check commit "Implement; commit on the branch"
  gears runbook step add single-ticket --kind ask --name review "Does this need a review? yes/no"
  gears runbook step add single-ticket --kind gate --check pushed --when review=yes "Wait for the push"`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookStepAdd(c, verbs.RunbookStepAddIn{Runbook: args[0], Kind: rbKind, Text: strings.Join(args[1:], " "), Check: rbCheck, Name: rbName, When: rbWhen})
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
			fmt.Printf("%-32s %-40s %-5s %d step%s\n", rb.Slug, rb.Title, rb.Target, len(rb.Steps), plural(len(rb.Steps), "", "s"))
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
	if r.Branch != "" {
		target += " @" + r.Branch
	}
	fmt.Printf("%s  %s%s  %s  %d/%d  %s\n", short(r.ID), r.Runbook, target, r.Agent, r.Done, r.Total, cur)
}

func printRun(r *verbs.Run) {
	fmt.Printf("run %s  %s", short(r.ID), r.Runbook)
	if r.Target != "" {
		fmt.Printf("  target %s", r.Target)
	}
	if r.Branch != "" {
		fmt.Printf("  branch %s", r.Branch)
	}
	fmt.Printf("  agent %s  %s\n", r.Agent, r.Status)
	marks := map[string]string{"pending": "·", "current": "▶", "done": "✓", "verified": "✓✓", "skipped": "↷"}
	for _, s := range r.Steps {
		line := fmt.Sprintf("  %-2s %02d  %-6s %s", marks[s.State], s.Idx, s.Kind, s.Text)
		if s.Check != "" {
			line += "  <" + s.Check + ">"
		}
		if s.When != "" {
			line += "  when " + s.When
		}
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
		fmt.Printf("%s  (%s, target %s)\n", out.Runbook.Title, out.Runbook.Slug, out.Runbook.Target)
		for _, s := range out.Runbook.Steps {
			check := ""
			if s.Check != "" {
				check = "  <" + s.Check + ">"
			}
			if s.Name != "" {
				check += "  name " + s.Name
			}
			if s.When != "" {
				check += "  when " + s.When
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

var runbookTakeoverCmd = &cobra.Command{
	Use:   "takeover --run <id> --reason <why>",
	Short: "Take another agent's active run",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunTakeover(c, verbs.RunTakeoverIn{RunRef: verbs.RunRef{Run: rbRun}, Reason: rbReason})
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

var runbookStepDoneCmd = &cobra.Command{
	Use:   "done [evidence]",
	Short: "Perform or verify the current step and advance",
	Long: `Evidence is what the step's check asks for: a branch (default: the
current one), a commit (default: HEAD), text to log for a session step, a
desk item id, an artifact file, or the answer for an ask. Card, gate, pushed,
merged and clean steps need none. Verification failure leaves the step current.`,
	Args: cobra.ArbitraryArgs,
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
	runbookStepAddCmd.Flags().StringVar(&rbCheck, "check", "", "check or check:arg for the kind (see runbook --help)")
	runbookStepAddCmd.Flags().StringVar(&rbName, "name", "", "name this step so a later --when can refer to its evidence")
	runbookStepAddCmd.Flags().StringVar(&rbWhen, "when", "", "condition name=value or name!=value; the step is skipped when it does not hold")
	runbookNewCmd.Flags().StringVar(&rbTargetKind, "target", "none", "card | repo | none")
	runbookStartCmd.Flags().StringVar(&rbTarget, "target", "", "the card key for a card runbook")
	for _, c := range []*cobra.Command{runbookStepDoneCmd, runbookSkipCmd, runbookAbandonCmd, runbookTakeoverCmd} {
		c.Flags().StringVar(&rbRun, "run", "", "run id (default: your active run)")
	}
	runbookSkipCmd.Flags().StringVar(&rbReason, "reason", "", "why the step is skipped (required)")
	runbookAbandonCmd.Flags().StringVar(&rbReason, "reason", "", "why the run is abandoned (required)")
	runbookTakeoverCmd.Flags().StringVar(&rbReason, "reason", "", "why you are taking the run (required)")

	runbookStepCmd.AddCommand(runbookStepAddCmd, runbookStepDoneCmd)
	runbookCmd.AddCommand(runbookNewCmd, runbookListCmd, runbookShowCmd, runbookStartCmd, runbookStepCmd, runbookSkipCmd, runbookTakeoverCmd, runbookAbandonCmd)
	root.AddCommand(runbookCmd)
}
