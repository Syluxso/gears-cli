package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	deskSendType string
	deskListType string
	deskTo       string
	deskRef      string
	deskAll      bool
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

func printDeskItems(items []verbs.DeskItem) {
	for _, it := range items {
		state := ""
		if it.State == "closed" {
			state = "  [closed]"
		}
		fmt.Printf("%s  %-8s %s → %s%s\n", short(it.ID), it.Type, it.From, orAnyone(it.To), state)
		fmt.Printf("          %s\n", it.Text)
		if it.Ref != "" {
			fmt.Printf("          ref %s\n", it.Ref)
		}
		if it.Answer != "" {
			fmt.Printf("          answer: %s\n", it.Answer)
		}
	}
}

var deskListCmd = &cobra.Command{
	Use:   "list",
	Short: "Open items for this agent (or anyone)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.DeskList(c, verbs.DeskListIn{All: deskAll, Type: deskListType})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Items) == 0 {
			fmt.Printf("desk is clear for %s\n", out.Agent)
			return nil
		}
		printDeskItems(out.Items)
		return nil
	},
}

var deskSendCmd = &cobra.Command{
	Use:   "send <text>",
	Short: "Place an item on a desk",
	Long: `  gears desk send --type ask --to grok-web "Is the branch GRS-14 ready to review?"
  gears desk send --type handoff "Tried X. Result Y. Next: Z."
  gears desk send --type info --to claude-local "API port is 8220."`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.DeskSend(c, verbs.DeskSendIn{Type: deskSendType, To: deskTo, Text: strings.Join(args, " "), Ref: deskRef})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printDeskItems([]verbs.DeskItem{*out})
		return nil
	},
}

// deskVerb builds the four closing commands, which differ only in the verb.
func deskVerb(use, short string, min int, fn func(verbs.Ctx, string, []string) (*verbs.DeskItem, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(min),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ctx()
			if err != nil {
				return err
			}
			out, err := fn(c, args[0], args[1:])
			if err != nil {
				return err
			}
			if emit(out) {
				return nil
			}
			printDeskItems([]verbs.DeskItem{*out})
			return nil
		},
	}
}

func init() {
	deskListCmd.Flags().BoolVar(&deskAll, "all", false, "include closed items")
	deskListCmd.Flags().StringVar(&deskListType, "type", "", "only this type")

	deskSendCmd.Flags().StringVarP(&deskSendType, "type", "t", "info", "info | ask | event | work | handoff")
	deskSendCmd.Flags().StringVar(&deskTo, "to", "", "agent label (default: anyone)")
	deskSendCmd.Flags().StringVar(&deskRef, "ref", "", "optional pointer: card key, run id, artifact file")

	read := deskVerb("read <id>", "Read an item; info closes when read", 1, func(c verbs.Ctx, id string, _ []string) (*verbs.DeskItem, error) {
		return verbs.DeskRead(c, verbs.DeskIDIn{ID: id})
	})
	answer := deskVerb("answer <id> <text>", "Close an ask with a reply", 2, func(c verbs.Ctx, id string, rest []string) (*verbs.DeskItem, error) {
		return verbs.DeskAnswer(c, verbs.DeskAnswerIn{ID: id, Answer: strings.Join(rest, " ")})
	})
	done := deskVerb("done <id>", "Close a work item", 1, func(c verbs.Ctx, id string, _ []string) (*verbs.DeskItem, error) {
		return verbs.DeskDone(c, verbs.DeskIDIn{ID: id})
	})
	dismiss := deskVerb("dismiss <id>", "Close an event", 1, func(c verbs.Ctx, id string, _ []string) (*verbs.DeskItem, error) {
		return verbs.DeskDismiss(c, verbs.DeskIDIn{ID: id})
	})

	deskCmd.AddCommand(deskListCmd, deskSendCmd, read, answer, done, dismiss)
	root.AddCommand(deskCmd)
}
