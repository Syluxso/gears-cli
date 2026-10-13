package cli

import (
	"fmt"
	"strconv"
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
	rbTitle      string
	rbFrom       string
	rbAs         string
	rbTo         int
	rbClearCheck bool
	rbClearName  bool
	rbClearWhen  bool
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

var runbookStepCmd = &cobra.Command{Use: "step", Short: "Author (add, update, remove, move) or complete (done) steps"}

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
			fmt.Printf("no runbooks. copy a built-in (%s): gears runbook copy single-ticket-development\nor create one: gears runbook new \"...\"\n", strings.Join(out.Builtins, ", "))
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
	fmt.Println(runSummaryLine(r))
}

func runSummaryLine(r verbs.RunSummary) string {
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
	return fmt.Sprintf("%s  %s%s  %s  %d/%d  %s", short(r.ID), r.Runbook, target, r.Agent, r.Done, r.Total, cur)
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
		printRunbook(out.Runbook)
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

func stepIdx(arg string) (int, error) {
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("step %q: want a step number from gears runbook show", arg)
	}
	return n, nil
}

func printRunbook(out *verbs.Runbook) {
	fmt.Printf("%s  (%s, target %s)\n", out.Title, out.Slug, out.Target)
	for _, s := range out.Steps {
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
}

var runbookStepUpdateCmd = &cobra.Command{
	Use:   "update <slug> <step> [text]",
	Short: "Edit a step in place (flags set; --clear-* unset)",
	Long: `Only the flags you pass change. Text, if given, replaces the step text.
  gears runbook step update single-ticket 5 --name review "Review needed? yes/no"
  gears runbook step update single-ticket 6 --check merged:main
  gears runbook step update single-ticket 6 --clear-when
Refused while a run is active on the runbook.`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		n, err := stepIdx(args[1])
		if err != nil {
			return err
		}
		in := verbs.RunbookStepUpdateIn{Runbook: args[0], Step: n}
		if len(args) > 2 {
			t := strings.Join(args[2:], " ")
			in.Text = &t
		}
		if cmd.Flags().Changed("kind") {
			in.Kind = &rbKind
		}
		if cmd.Flags().Changed("check") {
			in.Check = &rbCheck
		}
		if cmd.Flags().Changed("name") {
			in.Name = &rbName
		}
		if cmd.Flags().Changed("when") {
			in.When = &rbWhen
		}
		empty := ""
		if rbClearCheck {
			in.Check = &empty
		}
		if rbClearName {
			in.Name = &empty
		}
		if rbClearWhen {
			in.When = &empty
		}
		out, err := verbs.RunbookStepUpdate(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printRunbook(out)
		return nil
	},
}

var runbookStepRemoveCmd = &cobra.Command{
	Use:   "remove <slug> <step>",
	Short: "Remove a step; later steps renumber",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		n, err := stepIdx(args[1])
		if err != nil {
			return err
		}
		out, err := verbs.RunbookStepRemove(c, verbs.RunbookStepRemoveIn{Runbook: args[0], Step: n})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printRunbook(out)
		return nil
	},
}

var runbookStepMoveCmd = &cobra.Command{
	Use:   "move <slug> <step> --to <position>",
	Short: "Move a step to a new position",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		n, err := stepIdx(args[1])
		if err != nil {
			return err
		}
		if rbTo < 1 {
			return fmt.Errorf("--to <position> is required")
		}
		out, err := verbs.RunbookStepMove(c, verbs.RunbookStepMoveIn{Runbook: args[0], Step: n, To: rbTo})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printRunbook(out)
		return nil
	},
}

var runbookUpdateCmd = &cobra.Command{
	Use:   "update <slug> [--title ...] [--target card|repo|none]",
	Short: "Change a runbook's title or target (the slug stays)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		if rbTitle == "" && rbTargetKind == "" {
			return fmt.Errorf("pass --title and/or --target")
		}
		out, err := verbs.RunbookUpdate(c, verbs.RunbookUpdateIn{Runbook: args[0], Title: rbTitle, Target: rbTargetKind})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printRunbook(out)
		return nil
	},
}

var runbookDeleteCmd = &cobra.Command{
	Use:   "delete <slug>",
	Short: "Delete a runbook definition (refused while a run is active on it)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookDelete(c, verbs.RunbookDeleteIn{Runbook: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("runbook--%s.yaml deleted\n", out.Slug)
		return nil
	},
}

var runbookCopyCmd = &cobra.Command{
	Use:   "copy <slug> [--from <project dir>] [--as <title>]",
	Short: "Copy a built-in or another project's runbook into this one",
	Long: `Without --from, <slug> names a built-in (gears runbook list shows them).
  gears runbook copy single-ticket-development
  gears runbook copy single-ticket-development --from ../gears-cli --as "Ticket flow"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookCopy(c, verbs.RunbookCopyIn{Runbook: args[0], From: rbFrom, As: rbAs})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Println(out.Path)
		printRunbook(out)
		return nil
	},
}

func init() {
	runbookStepAddCmd.Flags().StringVarP(&rbKind, "kind", "k", "do", "ask | do | gears | git | gate")
	runbookStepAddCmd.Flags().StringVar(&rbCheck, "check", "", "check or check:arg for the kind (see runbook --help)")
	runbookStepAddCmd.Flags().StringVar(&rbName, "name", "", "name this step so a later --when can refer to its evidence")
	runbookStepAddCmd.Flags().StringVar(&rbWhen, "when", "", "condition name=value or name!=value; the step is skipped when it does not hold")
	runbookNewCmd.Flags().StringVar(&rbTargetKind, "target", "none", "card | repo | none")
	runbookUpdateCmd.Flags().StringVar(&rbTargetKind, "target", "", "card | repo | none")
	runbookUpdateCmd.Flags().StringVar(&rbTitle, "title", "", "new title")
	runbookStepUpdateCmd.Flags().StringVarP(&rbKind, "kind", "k", "", "ask | do | gears | git | gate")
	runbookStepUpdateCmd.Flags().StringVar(&rbCheck, "check", "", "check or check:arg")
	runbookStepUpdateCmd.Flags().StringVar(&rbName, "name", "", "step name for when:")
	runbookStepUpdateCmd.Flags().StringVar(&rbWhen, "when", "", "condition name=value or name!=value")
	runbookStepUpdateCmd.Flags().BoolVar(&rbClearCheck, "clear-check", false, "remove the check")
	runbookStepUpdateCmd.Flags().BoolVar(&rbClearName, "clear-name", false, "remove the name")
	runbookStepUpdateCmd.Flags().BoolVar(&rbClearWhen, "clear-when", false, "remove the condition")
	runbookStepMoveCmd.Flags().IntVar(&rbTo, "to", 0, "position the step lands on (1-based)")
	runbookCopyCmd.Flags().StringVar(&rbFrom, "from", "", "another project's root directory (default: built-in)")
	runbookCopyCmd.Flags().StringVar(&rbAs, "as", "", "title for the copy")
	runbookStartCmd.Flags().StringVar(&rbTarget, "target", "", "the card key for a card runbook")
	for _, c := range []*cobra.Command{runbookStepDoneCmd, runbookSkipCmd, runbookAbandonCmd, runbookTakeoverCmd} {
		c.Flags().StringVar(&rbRun, "run", "", "run id (default: your active run)")
	}
	runbookSkipCmd.Flags().StringVar(&rbReason, "reason", "", "why the step is skipped (required)")
	runbookAbandonCmd.Flags().StringVar(&rbReason, "reason", "", "why the run is abandoned (required)")
	runbookTakeoverCmd.Flags().StringVar(&rbReason, "reason", "", "why you are taking the run (required)")

	runbookStepCmd.AddCommand(runbookStepAddCmd, runbookStepUpdateCmd, runbookStepRemoveCmd, runbookStepMoveCmd, runbookStepDoneCmd)
	runbookCmd.AddCommand(runbookNewCmd, runbookCopyCmd, runbookUpdateCmd, runbookDeleteCmd, runbookListCmd, runbookShowCmd, runbookStartCmd, runbookStepCmd, runbookSkipCmd, runbookTakeoverCmd, runbookAbandonCmd)
	root.AddCommand(runbookCmd)
}
