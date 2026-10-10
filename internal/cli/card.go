package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	cardType       string
	cardAsA        string
	cardIWant      string
	cardSoThat     string
	cardAcceptance []string
	cardBody       string
	cardPriority   int
	cardArtifact   string
	cardSource     string
	cardData       string
	cardListState  string
	cardListType   string
	cardListAll    bool
	cardSetTitle   string
	cardSetBody    string
	cardSetType    string
	cardSetArt     string
	cardSetExt     string
	cardSetPri     int
)

var cardCmd = &cobra.Command{
	Use:   "card",
	Short: "The project's board: shaped cards in states",
	Long: `One board per project. Keys are <PREFIX>-<n> (the prefix is in
.gears/project.yaml). Every card needs a story and acceptance or the create
is refused, whatever its type.

  gears card new "Login with Google" --as-a "staff member" --i-want "to sign in with Google" \
      --so-that "I have no second password" --accept "Sign-in page shows a Google button" --accept "..."
  gears card list
  gears card show GRS-3
  gears card move GRS-3 "In Progress"`,
}

func printCard(c verbs.Card) {
	fmt.Printf("%-8s %-7s %-12s %s\n", c.Key, c.Type, c.State, c.Title)
}

func printCardFull(c verbs.Card) {
	fmt.Printf("%s  %s  [%s]  priority %d\n", c.Key, c.Title, c.State, c.Priority)
	fmt.Printf("type %s  by %s  created %s", c.Type, c.CreatedBy, hhmm(c.CreatedAt))
	if c.Artifact != "" {
		fmt.Printf("  artifact %s", c.Artifact)
	}
	if c.External != "" {
		fmt.Printf("  external %s", c.External)
	}
	fmt.Println()
	if st, ok := c.CardData["story"].(map[string]any); ok {
		fmt.Printf("\nAs a %s, I want %s, so that %s.\n", st["asA"], st["iWant"], st["soThat"])
	}
	if acc, ok := c.CardData["acceptance"].([]any); ok && len(acc) > 0 {
		fmt.Println("\nAcceptance")
		for _, a := range acc {
			fmt.Printf("  - %v\n", a)
		}
	}
	for _, k := range []string{"defect", "spike", "chore", "scenarios", "uat"} {
		v, ok := c.CardData[k]
		if !ok {
			continue
		}
		b, _ := json.MarshalIndent(v, "  ", "  ")
		fmt.Printf("\n%s\n  %s\n", k, string(b))
	}
	if c.Body != "" {
		fmt.Printf("\n%s\n", c.Body)
	}
	if len(c.Omitted) > 0 {
		fmt.Printf("\n(not yet: %s)\n", strings.Join(c.Omitted, ", "))
	}
}

func parseCardData(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("--data must be a JSON object: %w", err)
	}
	return m, nil
}

var cardNewCmd = &cobra.Command{
	Use:   "new <title>",
	Short: "Create a card (needs --as-a, --i-want, --so-that and at least one --accept)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		data, err := parseCardData(cardData)
		if err != nil {
			return err
		}
		out, err := verbs.CardNew(c, verbs.CardNewIn{Type: cardType, Title: strings.Join(args, " "),
			Story: verbs.CardStory{AsA: cardAsA, IWant: cardIWant, SoThat: cardSoThat}, Acceptance: cardAcceptance,
			Body: cardBody, CardData: data, Priority: cardPriority, Artifact: cardArtifact, Source: cardSource})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printCardFull(*out)
		return nil
	},
}

var cardListCmd = &cobra.Command{
	Use:   "list",
	Short: "The board",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardList(c, verbs.CardListIn{State: cardListState, Type: cardListType, All: cardListAll})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Cards) == 0 {
			fmt.Printf("no cards yet (prefix %s). Create one: gears card new ...\n", out.Prefix)
			return nil
		}
		for _, s := range out.States {
			if cardListState != "" && !strings.EqualFold(s.Name, cardListState) {
				continue
			}
			if s.Terminal && !cardListAll && cardListState == "" {
				continue
			}
			fmt.Printf("\n%s (%d)\n", s.Name, s.Count)
			for _, cd := range out.Cards {
				if cd.State == s.Name {
					fmt.Print("  ")
					printCard(cd)
				}
			}
		}
		return nil
	},
}

var cardShowCmd = &cobra.Command{
	Use:   "show <key>",
	Short: "One card with its sections",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardShow(c, verbs.CardKeyIn{Key: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printCardFull(*out)
		return nil
	},
}

var cardUpdateCmd = &cobra.Command{
	Use:   "update <key>",
	Short: "Change fields or sections; only flags given change",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		data, err := parseCardData(cardData)
		if err != nil {
			return err
		}
		in := verbs.CardUpdateIn{Key: args[0], CardData: data}
		f := cmd.Flags()
		if f.Changed("title") {
			in.Title = &cardSetTitle
		}
		if f.Changed("body") {
			in.Body = &cardSetBody
		}
		if f.Changed("type") {
			in.Type = &cardSetType
		}
		if f.Changed("priority") {
			in.Priority = &cardSetPri
		}
		if f.Changed("artifact") {
			in.Artifact = &cardSetArt
		}
		if f.Changed("external") {
			in.External = &cardSetExt
		}
		if f.Changed("as-a") || f.Changed("i-want") || f.Changed("so-that") {
			in.Story = &verbs.CardStory{AsA: cardAsA, IWant: cardIWant, SoThat: cardSoThat}
		}
		if f.Changed("accept") {
			in.Acceptance = cardAcceptance
		}
		out, err := verbs.CardUpdate(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printCardFull(*out)
		return nil
	},
}

var cardMoveCmd = &cobra.Command{
	Use:   "move <key> <state>",
	Short: "Move a card to a state by name",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardMove(c, verbs.CardMoveIn{Key: args[0], State: strings.Join(args[1:], " ")})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printCard(*out)
		return nil
	},
}

var cardDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Soft-delete a card; the number is never reused",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardDelete(c, verbs.CardKeyIn{Key: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("deleted %s  %s\n", out.Key, out.Title)
		return nil
	},
}

func init() {
	storyFlags := func(cmd *cobra.Command) {
		cmd.Flags().StringVar(&cardAsA, "as-a", "", "who wants this")
		cmd.Flags().StringVar(&cardIWant, "i-want", "", "what they want to do")
		cmd.Flags().StringVar(&cardSoThat, "so-that", "", "why it matters")
		cmd.Flags().StringArrayVar(&cardAcceptance, "accept", nil, "an acceptance line (repeatable)")
		cmd.Flags().StringVar(&cardData, "data", "", "extra sections as a JSON object (defect, spike, chore, scenarios, uat)")
	}
	storyFlags(cardNewCmd)
	cardNewCmd.Flags().StringVarP(&cardType, "type", "t", "story", "story | defect | spike | chore")
	cardNewCmd.Flags().StringVar(&cardBody, "body", "", "evidence: logs, snippets, links")
	cardNewCmd.Flags().IntVar(&cardPriority, "priority", 0, "higher is more urgent")
	cardNewCmd.Flags().StringVar(&cardArtifact, "artifact", "", "artifact file this card points at")
	cardNewCmd.Flags().StringVar(&cardSource, "source", "agent", "agent | user: who shaped the card")

	cardListCmd.Flags().StringVar(&cardListState, "state", "", "only this state")
	cardListCmd.Flags().StringVar(&cardListType, "type", "", "only this type")
	cardListCmd.Flags().BoolVar(&cardListAll, "all", false, "include completed cards")

	storyFlags(cardUpdateCmd)
	cardUpdateCmd.Flags().StringVar(&cardSetTitle, "title", "", "new title")
	cardUpdateCmd.Flags().StringVar(&cardSetBody, "body", "", "new body")
	cardUpdateCmd.Flags().StringVar(&cardSetType, "type", "", "story | defect | spike | chore")
	cardUpdateCmd.Flags().IntVar(&cardSetPri, "priority", 0, "higher is more urgent")
	cardUpdateCmd.Flags().StringVar(&cardSetArt, "artifact", "", "artifact file; empty clears")
	cardUpdateCmd.Flags().StringVar(&cardSetExt, "external", "", "Jira or Kanboard key; empty clears")

	cardCmd.AddCommand(cardNewCmd, cardListCmd, cardShowCmd, cardUpdateCmd, cardMoveCmd, cardDeleteCmd)
	root.AddCommand(cardCmd)
}
