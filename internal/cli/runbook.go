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
	rbRefresh    bool
	rbMine       bool
	rbBy         string
	rbRunbook    string
	rbLimit      int
	rbCursor     string
	rbOwner      string
	rbHook       bool
)

var runbookRunsCmd = &cobra.Command{
	Use:   "runs",
	Short: "Runs from the gears record: current step, progress, agent, card (no orchestrator needed)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookRuns(c, verbs.RunbookRunsIn{Status: rbStatus, Runbook: rbRunbook, Card: rbTarget, By: rbBy, Mine: rbMine, Owner: rbOwner,
			Limit: rbLimit, Cursor: rbCursor})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.Note != "" {
			fmt.Println("(" + out.Note + ")")
		}
		if len(out.Runs) == 0 {
			fmt.Println("no runs")
		}
		for _, r := range out.Runs {
			cur := "-"
			if r.CurrentStep != nil {
				cur = fmt.Sprintf("%d %s", r.CurrentStep.Position, r.CurrentStep.ID)
			}
			owner := r.Owner
			if owner != r.Agent {
				owner = r.Agent + ">" + owner // claimed: starter>owner
			}
			fmt.Printf("%s  %-24s %-10s %-5s %-8s %-16s %s\n", r.ID, r.Runbook, r.Status, r.Progress, r.Card, owner, cur)
		}
		if out.Next != "" {
			fmt.Println("more: --cursor " + out.Next)
		}
		return nil
	},
}

var runbookCmd = &cobra.Command{
	Use:   "runbook",
	Short: "Runbook runs on gears-api, and local runbook drafts",
	Long: `Runs (GRS-51) live in byz-orchestrator behind gears-api: start, complete,
skip, status and runs go to gears-api, which checks that evidence exists in
the workspace before a step advances; reads come from the gears record and
fall back to the local cache (gears sync pulls it) when gears-api is
offline.

Official runbooks (GRS-59) are frozen or editable: list shows them with the
adopted copies, show explains one, abilities is the step-kind catalog,
adopt copies one into the workspace on gears-api, edit changes an editable
copy, unadopt removes a copy. A frozen copy cannot be edited anywhere and
runs the official steps; start an adopted runbook with start <slug>. runs
lists runs from the gears record (no orchestrator needed).

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

var runbookStepCmd = &cobra.Command{Use: "step", Short: "Author the steps of a local draft: add, update, remove, move"}

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
	Short: "Official runbooks, this workspace's adopted copies, and local drafts",
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
		if out.Note != "" {
			fmt.Println("(" + out.Note + ")")
		}
		if len(out.Official) > 0 {
			fmt.Println("official")
			for _, o := range out.Official {
				line := fmt.Sprintf("  %-30s %-9s %d step%s  %s", o.Ref, frozenWord(o.Frozen), len(o.Steps), plural(len(o.Steps), "", "s"), o.Title)
				if len(o.Adopted) > 0 {
					line += "  (adopted as " + strings.Join(o.Adopted, ", ") + ")"
				}
				fmt.Println(line)
			}
		}
		fmt.Println("adopted")
		if len(out.Adopted) == 0 {
			fmt.Println("  none: gears runbook adopt <ref>")
		}
		for _, rb := range out.Adopted {
			fmt.Printf("  %-30s %-9s %d step%s  from %s\n", rb.Slug, frozenWord(rb.Frozen), len(rb.Steps), plural(len(rb.Steps), "", "s"), rb.SourceRef)
		}
		if len(out.Drafts) > 0 {
			fmt.Println("local drafts (not runnable)")
			for _, rb := range out.Drafts {
				fmt.Printf("  %-30s %-40s %-5s %d step%s\n", rb.Slug, rb.Title, rb.Target, len(rb.Steps), plural(len(rb.Steps), "", "s"))
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
	if r.Status == "canceled" {
		by := r.CanceledLabel
		if by == "" {
			by = r.CanceledVia
		}
		fmt.Printf("  stopped by %s: %s\n", by, r.CancelReason)
	}
	marks := map[string]string{"pending": "·", "current": "▶", "completed": "✓", "skipped": "↷", "canceled": "✕"}
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
		if s.DoneBySystem {
			line += "  done by system"
		} else if s.DoneLabel != "" {
			line += "  by " + s.DoneLabel
		}
		if s.Executor == "system" && !s.DoneBySystem {
			line += "  (system)"
		}
		if s.SystemError != "" {
			line += "  [system could not: " + s.SystemError + "]"
		}
		if s.Decision != nil {
			line += "  [" + s.Decision.Decision + ": " + s.Decision.Reason + "]"
		}
		fmt.Println(line)
	}
}

var runbookShowCmd = &cobra.Command{
	Use:   "show <slug|ref|run-id>",
	Short: "One runbook in full: an adopted slug or official ref, steps explained from the step-kind catalog",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookShow(c, verbs.RunbookShowIn{Runbook: args[0]})
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
		if out.Draft != nil {
			printRunbook(out.Draft)
			return nil
		}
		printDetail(&out.RunbookDetail)
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

var runbookStopCmd = &cobra.Command{
	Use:   "stop <run> --reason <why>",
	Short: "Stop (cancel) a running run, with a reason (through gears-api)",
	Long: `Cancels a running run through gears-api, which asks byz-orchestrator to
cancel it: the current step and the run become canceled, later steps stay
pending, and the reason stays on the run. <run> is the run id or a prefix of
a cached one. A finished run is refused.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookStop(c, verbs.RunbookStopIn{Run: args[0], Reason: rbReason})
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

var runbookClaimCmd = &cobra.Command{
	Use:   "claim <run>",
	Short: "Take over a running run: this agent label becomes its owner (GRS-91)",
	Long: `Makes the label you act as (-a, $GEARS_AGENT, the workspace's agent name)
the owner of a running run on gears-api. The owner is the session kept
working on the run: the Stop hook (gears runbook next --hook) and the
default run of runbook next, complete and skip. Any member of the workspace
may claim; the move is on the run's timeline as run.claimed (from, to).
<run> is the run id or a prefix of a cached one.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookClaim(c, verbs.RunbookClaimIn{Run: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if !out.Claimed {
			fmt.Printf("%s already owns %s\n", out.To, out.Run.ID)
			return nil
		}
		fmt.Printf("claimed %s: owner %s (was %s)\n", out.Run.ID, out.To, out.From)
		return nil
	},
}

var runbookNextCmd = &cobra.Command{
	Use:   "next [run]",
	Short: "What to do now on a run: the current step, who it waits on, instructions (GRS-87)",
	Long: `Asks gears-api what to do now on a run (no orchestrator needed): the
current step with its kind, executor (system or agent) and ability, a
one-line instruction, and whether it waits on you (agent), the system or a
person (human). Omit run for the one running run you own.

--hook is the Claude Code Stop hook (gears hooks install writes it): it reads
the hook JSON on stdin and, only when a running run this agent label OWNS
waits on the agent, prints {"decision":"block","reason":"<the step and what
to do>"} so the agent keeps going. Otherwise (no run, finished, waiting on a
person or the system, already continuing without progress, deleted
workspace, offline, not logged in, any error, over ~2s) it prints nothing.
It always exits 0 and is silent on stderr unless GEARS_HOOK_DEBUG=1.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if rbHook {
			runStopHook()
			return nil
		}
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.RunbookNextIn{}
		if len(args) == 1 {
			in.Run = args[0]
		}
		out, err := verbs.RunbookNext(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.Note != "" {
			fmt.Println("(" + out.Note + ")")
		}
		if len(out.Runs) > 0 {
			fmt.Println(out.Message)
			for _, r := range out.Runs {
				fmt.Printf("  %s  %s  %s  %s\n", r.ID, r.Runbook, r.Card, r.Progress)
			}
			return nil
		}
		fmt.Printf("run %s  %s  %s  %d/%d", out.RunID, out.Runbook, out.Status, out.Done, out.Total)
		if out.Card != "" {
			fmt.Printf("  card %s", out.Card)
		}
		fmt.Println()
		if out.Step != nil {
			fmt.Printf("  step %s (%s, %s)  waiting on %s\n", out.Step.ID, out.Step.Kind, out.Step.Executor, out.WaitingOn)
		}
		fmt.Println("  " + out.Instructions)
		if len(out.Next) > 0 {
			fmt.Println("  then: " + strings.Join(out.Next, " / "))
		}
		return nil
	},
}

var runbookHistoryCmd = &cobra.Command{
	Use:   "history <run>",
	Short: "A run's timeline: started, each step done or skipped (evidence, who), completed or canceled",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.RunbookHistory(c, verbs.RunbookHistoryIn{Run: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.Note != "" {
			fmt.Println("(" + out.Note + ")")
		}
		fmt.Printf("run %s  %s  %s  v%d\n", out.RunID, out.Runbook, out.Status, out.Version)
		if len(out.Events) == 0 {
			fmt.Println("  no events recorded")
		}
		for _, e := range out.Events {
			fmt.Println("  " + historyLine(e))
		}
		return nil
	},
}

// historyLine is one timeline event: when, version, type, step, who, and
// its evidence, reason or note.
func historyLine(e api.RunEvent) string {
	line := fmt.Sprintf("%s  v%-3d %-15s", e.At.Local().Format("2006-01-02 15:04:05"), e.Version, e.Type)
	if e.StepID != "" {
		line += " " + e.StepID
	}
	who := e.Label
	if who == "" {
		who = e.Via
	}
	if who != "" {
		line += "  by " + who
	}
	for _, ev := range e.Evidence {
		line += "  [" + ev.Kind + " " + ev.Ref + "]"
	}
	if e.Type == "run.claimed" {
		line += "  " + e.From + " -> " + e.To
	}
	if e.Reason != "" {
		line += "  (" + e.Reason + ")"
	}
	if e.Note != "" {
		line += "  note: " + e.Note
	}
	return line
}

var runbookStatusCmd = &cobra.Command{
	Use:   "status [run]",
	Short: "A run's steps from the gears record (--refresh: live from the orchestrator), or the workspace's runs",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.RunbookStatusIn{Status: rbStatus, Card: rbTarget, Refresh: rbRefresh}
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
	runbookStopCmd.Flags().StringVar(&rbReason, "reason", "", "why the run is stopped (required)")
	runbookStatusCmd.Flags().StringVar(&rbStatus, "status", "", "list filter: running, completed, ...")
	runbookStatusCmd.Flags().StringVar(&rbTarget, "card", "", "list filter: card key")
	runbookStatusCmd.Flags().BoolVar(&rbRefresh, "refresh", false, "one run: read the live state from the orchestrator first (stale when it cannot)")
	runbookRunsCmd.Flags().StringVar(&rbStatus, "status", "", "running (or active), completed, failed, canceled, ...")
	runbookRunsCmd.Flags().StringVar(&rbRunbook, "runbook", "", "the runbook's name or ref")
	runbookRunsCmd.Flags().StringVar(&rbTarget, "card", "", "card key")
	runbookRunsCmd.Flags().StringVar(&rbBy, "by", "", "agent label that started the run")
	runbookRunsCmd.Flags().BoolVar(&rbMine, "mine", false, "only runs this agent started")
	runbookRunsCmd.Flags().StringVar(&rbOwner, "owner", "", "only runs this label owns (me: this agent)")
	runbookNextCmd.Flags().BoolVar(&rbHook, "hook", false, "Claude Code Stop hook mode: block only when a run this label owns waits on the agent")
	runbookRunsCmd.Flags().IntVar(&rbLimit, "limit", 0, "page size (default 50)")
	runbookRunsCmd.Flags().StringVar(&rbCursor, "cursor", "", "next page, from the previous output")

	runbookStepCmd.AddCommand(runbookStepAddCmd, runbookStepUpdateCmd, runbookStepRemoveCmd, runbookStepMoveCmd)
	runbookCmd.AddCommand(runbookNewCmd, runbookCopyCmd, runbookUpdateCmd, runbookDeleteCmd, runbookListCmd, runbookShowCmd, runbookStartCmd,
		runbookStepCmd, runbookCompleteCmd, runbookSkipCmd, runbookStopCmd, runbookClaimCmd, runbookNextCmd, runbookHistoryCmd, runbookStatusCmd, runbookRunsCmd)
	root.AddCommand(runbookCmd)
}
