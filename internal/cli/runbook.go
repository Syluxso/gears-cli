package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/api"
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
	rbSteps      []string
	rbKey        string
	rbStep       string
	rbNote       string
	rbStatus     string
	rbClearCheck bool
	rbClearName  bool
	rbClearWhen  bool
)

var runbookCmd = &cobra.Command{
	Use:   "runbook",
	Short: "Runbook runs on gears-api, and local runbook drafts",
	Long: `Runs (GRS-51) live in byz-orchestrator behind gears-api: start, complete,
skip and status go to gears-api, which checks that evidence exists in the
workspace before a step advances. list and show read the local cache of the
gears record (gears sync pulls it); status reads live.

Official runbooks (GRS-59) are frozen or editable: catalog lists them, adopt
copies one into the workspace on gears-api, edit changes an editable copy.
A frozen copy cannot be edited anywhere and runs the official steps; start
an adopted runbook with start <slug>.

The local run engine is retired. new, copy, update, delete and step
add/update/remove/move still author runbook--<slug>.yaml drafts in
.gears/artifacts/, in the old local vocabulary.

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
		if len(out.Adopted) > 0 {
			fmt.Println("\nadopted (official copies)")
			for _, rb := range out.Adopted {
				fmt.Printf("%-32s %-40s %-9s %d step%s  from %s\n", rb.Slug, rb.Title, frozenWord(rb.Frozen), len(rb.Steps), plural(len(rb.Steps), "", "s"), rb.SourceRef)
			}
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

func printRun(r *api.Run) {
	fmt.Printf("run %s  %s  %s  v%d", r.ID, r.Runbook, r.Status, r.Version)
	if r.CardKey != "" {
		fmt.Printf("  card %s", r.CardKey)
	}
	if r.AgentLabel != "" {
		fmt.Printf("  agent %s", r.AgentLabel)
	}
	fmt.Println()
	marks := map[string]string{"pending": "·", "current": "▶", "completed": "✓", "skipped": "↷"}
	for _, s := range r.Steps {
		line := fmt.Sprintf("  %-2s %02d  %-12s %s", marks[s.Status], s.Position, s.Kind, s.StepID)
		if s.Title != "" && s.Title != s.StepID {
			line += "  " + s.Title
		}
		for _, e := range s.Evidence {
			line += "  [" + e.Kind + " " + e.Ref + "]"
		}
		if s.SkipReason != "" {
			line += "  (" + s.SkipReason + ")"
		}
		if s.DoneLabel != "" {
			line += "  by " + s.DoneLabel
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
		if out.Adopted != nil {
			printAdopted(out.Adopted)
			return nil
		}
		printRunbook(out.Runbook)
		return nil
	},
}

var runbookStartCmd = &cobra.Command{
	Use:   "start <runbook>",
	Short: "Start a run on gears-api (a registered runbook, or --step inline steps)",
	Long: `Starts a run through gears-api, which hands it to byz-orchestrator (GRS-51).
<runbook> is a registered runbook's name, e.g. gears-migrate. With --step
(repeatable, id or id:kind) the steps are sent inline under that name; kinds
come from the orchestrator's ability catalog. --card ties the run to a card.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.RunbookStartIn{Runbook: args[0], Card: rbTarget, IdempotencyKey: rbKey}
		for _, st := range rbSteps {
			id, kind, _ := strings.Cut(st, ":")
			in.Steps = append(in.Steps, api.StepDef{ID: id, Kind: kind})
		}
		out, err := verbs.RunbookStart(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printRun(out.Run)
		return nil
	},
}

func printStepOut(out *api.RunStepOut) {
	fmt.Printf("step %s %s; run %s", out.StepID, out.StepStatus, out.RunStatus)
	if out.CurrentStep != "" {
		fmt.Printf(", current %s", out.CurrentStep)
	}
	fmt.Println()
	for _, n := range out.Notes {
		fmt.Println("note: " + n)
	}
}

// parseEvidence reads kind:ref, or kind:ref:state for card and desk.
func parseEvidence(args []string) ([]api.Evidence, error) {
	var out []api.Evidence
	for _, a := range args {
		kind, rest, ok := strings.Cut(a, ":")
		if !ok || rest == "" {
			return nil, fmt.Errorf("evidence %q: write kind:ref (session, artifact, commit, desk, card); card:KEY:State for a state", a)
		}
		e := api.Evidence{Kind: kind, Ref: rest}
		if kind == "card" || kind == "desk" {
			if ref, state, ok := strings.Cut(rest, ":"); ok {
				e.Ref, e.Detail = ref, map[string]any{"state": state}
			}
		}
		out = append(out, e)
	}
	return out, nil
}

var runbookCompleteCmd = &cobra.Command{
	Use:   "complete [kind:ref ...]",
	Short: "Complete a run's current step with evidence (through gears-api)",
	Long: `Evidence items are kind:ref: session:<entry id or short>,
artifact:<file name or id>, desk:<id>[:state], card:<KEY>[:<State>],
commit:<sha>. gears-api checks each exists in the workspace (sync first);
a refusal leaves the step current. --run defaults to your one running run,
--step to its current step.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		ev, err := parseEvidence(args)
		if err != nil {
			return err
		}
		out, err := verbs.RunbookStepComplete(c, verbs.RunbookStepCompleteIn{RunPick: verbs.RunPick{Run: rbRun, Step: rbStep, Note: rbNote}, Evidence: ev})
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
	Use:   "skip --reason <why>",
	Short: "Skip a run's current step, with a reason (through gears-api)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookStepSkip(c, verbs.RunbookStepSkipIn{RunPick: verbs.RunPick{Run: rbRun, Step: rbStep, Note: rbNote}, Reason: rbReason})
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

var runbookStatusCmd = &cobra.Command{
	Use:   "status [run]",
	Short: "A run's steps live from gears-api, or the workspace's runs",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.RunbookStatusIn{Status: rbStatus, Card: rbTarget}
		if len(args) == 1 {
			in.Run = args[0]
		}
		out, err := verbs.RunbookStatus(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.Note != "" {
			fmt.Println("(" + out.Note + ")")
		}
		if out.Run != nil {
			printRun(out.Run)
			return nil
		}
		if len(out.Runs) == 0 {
			fmt.Println("no runs")
		}
		for _, r := range out.Runs {
			cur := r.CurrentStep
			if cur == "" {
				cur = "-"
			}
			fmt.Printf("%s  %-20s %-10s %-8s %-14s %s\n", r.ID, r.Runbook, r.Status, r.CardKey, r.AgentLabel, cur)
		}
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
	runbookStartCmd.Flags().StringVar(&rbTarget, "card", "", "the card the run is for, e.g. GRS-55")
	runbookStartCmd.Flags().StringArrayVar(&rbSteps, "step", nil, "inline step id or id:kind (repeatable, in order)")
	runbookStartCmd.Flags().StringVar(&rbKey, "key", "", "idempotency key: the same key returns the same run")
	for _, c := range []*cobra.Command{runbookCompleteCmd, runbookSkipCmd} {
		c.Flags().StringVar(&rbRun, "run", "", "run id (default: your one running run)")
		c.Flags().StringVar(&rbStep, "step", "", "step id (default: the current step)")
		c.Flags().StringVar(&rbNote, "note", "", "a note kept on the step")
	}
	runbookSkipCmd.Flags().StringVar(&rbReason, "reason", "", "why the step is skipped (required)")
	runbookStatusCmd.Flags().StringVar(&rbStatus, "status", "", "list filter: running, completed, ...")
	runbookStatusCmd.Flags().StringVar(&rbTarget, "card", "", "list filter: card key")

	runbookStepCmd.AddCommand(runbookStepAddCmd, runbookStepUpdateCmd, runbookStepRemoveCmd, runbookStepMoveCmd)
	runbookCmd.AddCommand(runbookNewCmd, runbookCopyCmd, runbookUpdateCmd, runbookDeleteCmd, runbookListCmd, runbookShowCmd, runbookStartCmd,
		runbookStepCmd, runbookCompleteCmd, runbookSkipCmd, runbookStatusCmd)
	root.AddCommand(runbookCmd)
}
