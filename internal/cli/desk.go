package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/desk"
	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/session"
)

var (
	deskType     string
	deskListType string
	deskTo       string
	deskFrom     string
	deskRef      string
	deskAll      bool
	deskAgent    string
)

var deskCmd = &cobra.Command{
	Use:   "desk",
	Short: "An agent's inbox and workspace",
	Long: `Items on a desk come from you, from other agents, and from Gears itself.
The type of an item is its lifecycle:

  info     context. closes when read         gears desk read <id>
  ask      a question. closes when answered  gears desk answer <id> "..."
  event    a webhook. closes when dismissed  gears desk dismiss <id>
  work     a task. closes when done          gears desk done <id>
  handoff  tried, result, next. never closes`,
}

var deskListCmd = &cobra.Command{
	Use:   "list",
	Short: "Open items for this agent (or anyone)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		agent := deskAgent
		if agent == "" {
			agent = session.DefaultAgent()
		}
		items, err := desk.List(p, desk.Filter{Agent: agent, All: deskAll, Type: deskListType})
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Printf("desk is clear for %s\n", agent)
			return nil
		}
		printDesk(items)
		return nil
	},
}

func printDesk(items []desk.Item) {
	for _, it := range items {
		to := it.To
		if to == "" {
			to = "anyone"
		}
		state := ""
		if it.State == "closed" {
			state = "  [closed]"
		}
		fmt.Printf("%s  %-8s %s → %s%s\n", it.Short(), it.Type, it.From, to, state)
		fmt.Printf("          %s\n", it.Text)
		if it.Ref != "" {
			fmt.Printf("          ref %s\n", it.Ref)
		}
		if it.Answer != "" {
			fmt.Printf("          answer: %s\n", it.Answer)
		}
	}
}

var deskSendCmd = &cobra.Command{
	Use:   "send <text>",
	Short: "Place an item on a desk",
	Long: `  gears desk send --type ask --to grok-web "Is the branch GRS-14 ready to review?"
  gears desk send --type handoff "Tried X. Result Y. Next: Z."
  gears desk send --type info --to claude-local "API port is 8220."`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := project.Find(mustCwd())
		if err != nil {
			return err
		}
		it, err := desk.Send(p, deskType, deskFrom, deskTo, strings.Join(args, " "), deskRef)
		if err != nil {
			return err
		}
		printDesk([]desk.Item{*it})
		return nil
	},
}

func deskVerb(use, short string, fn func(p *project.Project, key, agent string, args []string) (*desk.Item, error), min int) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(min),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Find(mustCwd())
			if err != nil {
				return err
			}
			it, err := fn(p, args[0], deskAgent, args[1:])
			if err != nil {
				return err
			}
			printDesk([]desk.Item{*it})
			return nil
		},
	}
}

func init() {
	deskListCmd.Flags().BoolVar(&deskAll, "all", false, "include closed items")
	deskListCmd.Flags().StringVar(&deskListType, "type", "", "only this type")

	deskSendCmd.Flags().StringVarP(&deskType, "type", "t", "info", "info | ask | event | work | handoff")
	deskSendCmd.Flags().StringVar(&deskTo, "to", "", "agent label (default: anyone)")
	deskSendCmd.Flags().StringVar(&deskFrom, "from", "", "sender label (default: $GEARS_AGENT or \"local\")")
	deskSendCmd.Flags().StringVar(&deskRef, "ref", "", "optional pointer: card key, run id, artifact file")

	read := deskVerb("read <id>", "Read an item; info closes when read", func(p *project.Project, key, agent string, _ []string) (*desk.Item, error) {
		return desk.Read(p, key, agent)
	}, 1)
	answer := deskVerb("answer <id> <text>", "Close an ask with a reply", func(p *project.Project, key, agent string, rest []string) (*desk.Item, error) {
		return desk.Answer(p, key, agent, strings.Join(rest, " "))
	}, 2)
	done := deskVerb("done <id>", "Close a work item", func(p *project.Project, key, agent string, _ []string) (*desk.Item, error) {
		return desk.Done(p, key, agent)
	}, 1)
	dismiss := deskVerb("dismiss <id>", "Close an event", func(p *project.Project, key, agent string, _ []string) (*desk.Item, error) {
		return desk.Dismiss(p, key, agent)
	}, 1)

	deskCmd.PersistentFlags().StringVarP(&deskAgent, "agent", "a", "", "act as this agent (default: $GEARS_AGENT or \"local\")")
	deskCmd.AddCommand(deskListCmd, deskSendCmd, read, answer, done, dismiss)
	root.AddCommand(deskCmd)
}
