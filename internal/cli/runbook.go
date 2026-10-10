package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/runbook"
)

var (
	rbKind   string
	rbCheck  string
	rbTarget string
	rbRun    string
	rbAgent  string
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
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		rb, err := runbook.New(p, strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Printf("%s\nadd steps: gears runbook step add %s --kind ask \"...\"\n", rb.Path, rb.Slug)
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
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		rb, err := runbook.Load(p, args[0])
		if err != nil {
			return err
		}
		if err := rb.AddStep(rbKind, strings.Join(args[1:], " "), rbCheck); err != nil {
			return err
		}
		fmt.Printf("%s: %d step%s\n", rb.Slug, len(rb.Steps), plural(len(rb.Steps), "", "s"))
		return nil
	},
}

var runbookListCmd = &cobra.Command{
	Use:   "list",
	Short: "Runbooks in this project, and active runs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		books, err := runbook.All(p)
		if err != nil {
			return err
		}
		if len(books) == 0 {
			fmt.Println("no runbooks. create one: gears runbook new \"Single ticket development\"")
		}
		for _, rb := range books {
			fmt.Printf("%-32s %-40s %d step%s\n", rb.Slug, rb.Title, len(rb.Steps), plural(len(rb.Steps), "", "s"))
		}
		runs, err := runbook.Active(p)
		if err != nil {
			return err
		}
		if len(runs) > 0 {
			fmt.Println("\nactive runs")
			for _, r := range runs {
				printRunLine(r)
			}
		}
		return nil
	},
}

func printRunLine(r runbook.Run) {
	done := 0
	for _, s := range r.Steps {
		if s.State != "pending" && s.State != "current" {
			done++
		}
	}
	cur := "—"
	if s := r.Current(); s != nil {
		cur = fmt.Sprintf("step %d: %s", s.Idx, s.Text)
	}
	target := r.Target
	if target != "" {
		target = " " + target
	}
	fmt.Printf("%s  %s%s  %s  %d/%d  %s\n", r.Short(), r.Runbook, target, r.Agent, done, len(r.Steps), cur)
}

var runbookShowCmd = &cobra.Command{
	Use:   "show <slug|run-id>",
	Short: "Show a runbook's steps, or a run's progress",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		if r, err := runbook.Get(p, args[0]); err == nil {
			printRun(r)
			return nil
		}
		rb, err := runbook.Load(p, args[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s  (%s)\n", rb.Title, rb.Slug)
		for i, s := range rb.Steps {
			check := ""
			if s.Check != "" {
				check = "  evidence: " + s.Check
			}
			fmt.Printf("  %02d  %-6s %s%s\n", i+1, s.Kind, s.Text, check)
		}
		return nil
	},
}

func printRun(r *runbook.Run) {
	fmt.Printf("run %s  %s", r.Short(), r.Runbook)
	if r.Target != "" {
		fmt.Printf("  target %s", r.Target)
	}
	fmt.Printf("  agent %s  %s\n", r.Agent, r.Status)
	for _, s := range r.Steps {
		mark := map[string]string{"pending": "·", "current": "▶", "done": "✓", "verified": "✓✓", "skipped": "↷"}[s.State]
		line := fmt.Sprintf("  %-2s %02d  %-6s %s", mark, s.Idx, s.Kind, s.Text)
		if s.Evidence != "" {
			line += "  [" + s.Evidence + "]"
		}
		if s.Reason != "" {
			line += "  (" + s.Reason + ")"
		}
		if !s.DoneAt.IsZero() {
			line += "  " + s.DoneAt.Format("15:04")
		}
		fmt.Println(line)
	}
}

var runbookStartCmd = &cobra.Command{
	Use:   "start <slug>",
	Short: "Start a run; its first step lands on the desk",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		r, err := runbook.Start(p, args[0], rbTarget, rbAgent)
		if err != nil {
			return err
		}
		printRun(r)
		return nil
	},
}

func resolveRun(p *project.Project) (*runbook.Run, error) {
	if rbRun != "" {
		return runbook.Get(p, rbRun)
	}
	return runbook.Only(p)
}

var runbookStepDoneCmd = &cobra.Command{
	Use:   "done <evidence>",
	Short: "Complete the current step with evidence",
	Long: `Evidence is what the step's check asks for: a branch name, a commit,
a session entry id, a desk item id, an artifact file, or a note for steps
without a check. Verification failure leaves the step current.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		r, err := resolveRun(p)
		if err != nil {
			return err
		}
		out, err := runbook.Complete(p, r.ID, strings.Join(args, " "), rbAgent)
		if err != nil {
			return err
		}
		state := "done"
		if out.Verified {
			state = "verified"
		}
		fmt.Printf("step %d %s (%s)\n", out.Step.Idx, state, out.Note)
		if out.Finished {
			fmt.Printf("run %s completed\n", r.Short())
		} else {
			r, _ = runbook.Get(p, r.ID)
			if s := r.Current(); s != nil {
				fmt.Printf("next: step %d  %-6s %s\n", s.Idx, s.Kind, s.Text)
			}
		}
		return nil
	},
}

var runbookSkipCmd = &cobra.Command{
	Use:   "skip",
	Short: "Skip the current step, with a reason",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		r, err := resolveRun(p)
		if err != nil {
			return err
		}
		out, err := runbook.Skip(p, r.ID, rbReason, rbAgent)
		if err != nil {
			return err
		}
		fmt.Printf("step %d skipped: %s\n", out.Step.Idx, out.Step.Reason)
		if out.Finished {
			fmt.Printf("run %s completed (with skips)\n", r.Short())
		}
		return nil
	},
}

var runbookAbandonCmd = &cobra.Command{
	Use:   "abandon",
	Short: "End a run without completing it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		r, err := resolveRun(p)
		if err != nil {
			return err
		}
		r, err = runbook.Abandon(p, r.ID, rbReason, rbAgent)
		if err != nil {
			return err
		}
		fmt.Printf("run %s abandoned\n", r.Short())
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

	runbookCmd.PersistentFlags().StringVarP(&rbAgent, "agent", "a", "", "act as this agent (default: $GEARS_AGENT or \"local\")")
	runbookStepCmd.AddCommand(runbookStepAddCmd, runbookStepDoneCmd)
	runbookCmd.AddCommand(runbookNewCmd, runbookListCmd, runbookShowCmd, runbookStartCmd, runbookStepCmd, runbookSkipCmd, runbookAbandonCmd)
	root.AddCommand(runbookCmd)
}
