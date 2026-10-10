package cli

import (
	"encoding/json"
	"fmt"
	"os"
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
	cardSize       string
	cardPriority   string
	cardTags       []string
	cardAssignee   string
	cardParent     string
	cardDue        string
	cardEstimate   int
	cardArtifact   string
	cardSource     string
	cardSections   string
	cardSet        []string

	cardListState    string
	cardListType     string
	cardListTag      string
	cardListAssignee string
	cardListParent   string
	cardListQuery    string
	cardListAll      bool

	cardSetTitle string
	cardSetBody  string
	cardSetType  string
	cardSetArt   string
	cardSetExt   string

	cardMovePos  string
	cardLinkKind string
	cardLinkURL  string
	cardLinkTtl  string
	cardLinkRm   string
	cardCheckOp  string
	cardCheckLst string
	cardStateWIP int
)

var cardCmd = &cobra.Command{
	Use:   "card",
	Short: "The project's board: shaped cards in states",
	Long: `One board per project. Keys are <PREFIX>-<n>; the prefix is in
.gears/project.yaml. A card is a founder-scannable top line plus an
agent-executable brief. Every card needs its type's top-line section and at
least one acceptance line, or the create is refused.

  gears card shapes
  gears card new "Login with Google" --as-a "staff member" --i-want "to sign in with Google" \
      --so-that "I have no second password" --accept "Google button shows, proven by a screenshot" \
      --size S --priority P2 --tag auth
  gears card new -t defect "Drafts fail to send" --set defect.symptom="drafts fail" \
      --set defect.expected=sent --set defect.actual="500" --accept "Send succeeds"
  gears card list --tag auth
  gears card show GRS-3
  gears card claim GRS-3
  gears card comment GRS-3 "Tried X; result Y."
  gears card move GRS-3 Testing`,
}

// parseSet turns --set section.field=value into nested sections. A value of
// a,b,c on a list field becomes a list.
func parseSet(sets []string, base map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for _, s := range sets {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, fmt.Errorf("--set %q: want section.field=value", s)
		}
		path, val := s[:eq], s[eq+1:]
		parts := strings.SplitN(path, ".", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("--set %q: want section.field=value", s)
		}
		sec, _ := out[parts[0]].(map[string]any)
		if sec == nil {
			sec = map[string]any{}
		}
		switch parts[1] {
		case "refs", "files", "prereqs", "rules", "gotchas":
			var list []any
			for _, x := range strings.Split(val, ",") {
				if x = strings.TrimSpace(x); x != "" {
					list = append(list, x)
				}
			}
			sec[parts[1]] = list
		case "timeboxMinutes":
			var n int
			fmt.Sscanf(val, "%d", &n)
			sec[parts[1]] = n
		default:
			sec[parts[1]] = val
		}
		out[parts[0]] = sec
	}
	return out, nil
}

func parseSections(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("--sections must be a JSON object: %w", err)
	}
	return m, nil
}

func printCardLine(c verbs.Card) {
	meta := []string{c.Type}
	if c.Size != "" {
		meta = append(meta, c.Size)
	}
	meta = append(meta, c.Priority)
	if c.Assignee != "" {
		meta = append(meta, "@"+c.Assignee)
	}
	if len(c.Tags) > 0 {
		meta = append(meta, "#"+strings.Join(c.Tags, " #"))
	}
	fmt.Printf("%-8s %s  [%s]\n", c.Key, c.Title, strings.Join(meta, " "))
}

func printCardFull(f *verbs.CardFull) {
	c := f.Card
	printCardLine(c)
	fmt.Printf("         %s  by %s  %s", c.State, c.CreatedBy, hhmm(c.CreatedAt))
	if c.Parent != "" {
		fmt.Printf("  parent %s", c.Parent)
	}
	if c.DueAt != "" {
		fmt.Printf("  due %s", c.DueAt[:10])
	}
	if c.Artifact != "" {
		fmt.Printf("  artifact %s", c.Artifact)
	}
	if c.External != "" {
		fmt.Printf("  external %s", c.External)
	}
	fmt.Println()
	fmt.Println()
	fmt.Println(c.TopLine)
	if acc, ok := c.CardData["acceptance"].([]any); ok && len(acc) > 0 {
		for _, a := range acc {
			fmt.Printf("Done when: %v\n", a)
		}
	}
	if len(f.BlockedBy) > 0 {
		fmt.Printf("Blocked by: %s\n", strings.Join(f.BlockedBy, ", "))
	}
	if brief, ok := c.CardData["brief"].(map[string]any); ok && filledAny(brief) {
		fmt.Println("\n---")
		for _, k := range []string{"state", "refs", "files", "prereqs", "rules", "gotchas", "escalate"} {
			v := brief[k]
			if !filledAny(v) {
				continue
			}
			switch t := v.(type) {
			case []any:
				var parts []string
				for _, x := range t {
					parts = append(parts, fmt.Sprint(x))
				}
				fmt.Printf("%-9s %s\n", strings.Title(k)+":", strings.Join(parts, " · "))
			default:
				fmt.Printf("%-9s %v\n", strings.Title(k)+":", t)
			}
		}
	}
	for _, k := range []string{"defect", "task", "spike", "chore", "epic", "scenarios", "uat"} {
		v, ok := c.CardData[k]
		if !ok || !filledAny(v) || k == f.Card.Type || (k == "defect" && c.Type == "defect") {
			continue
		}
		b, _ := json.MarshalIndent(v, "  ", "  ")
		fmt.Printf("\n%s\n  %s\n", k, string(b))
	}
	if c.Body != "" {
		fmt.Printf("\n--- original ---\n%s\n", c.Body)
	}
	if len(f.Checklists) > 0 {
		for _, cl := range f.Checklists {
			fmt.Printf("\n%s (%d/%d)\n", cl.Title, cl.Done, cl.Total)
			for i, it := range cl.Items {
				mark := "[ ]"
				if it.Done {
					mark = "[x]"
				}
				fmt.Printf("  %d. %s %s\n", i+1, mark, it.Title)
			}
		}
	}
	if len(f.Links) > 0 {
		fmt.Println("\nLinks")
		for _, l := range f.Links {
			fmt.Printf("  %s %s %s%s %s\n", short(l.ID), l.Kind, l.Target, l.URL, l.Title)
		}
	}
	if len(f.Children) > 0 {
		fmt.Println("\nChildren")
		for _, ch := range f.Children {
			fmt.Printf("  %-8s %-12s %s\n", ch.Key, ch.State, ch.Title)
		}
	}
	if len(f.Comments) > 0 {
		fmt.Println("\nComments")
		for _, cm := range f.Comments {
			fmt.Printf("  %s %s: %s\n", hhmm(cm.CreatedAt), cm.Agent, cm.Body)
		}
	}
	if len(f.Events) > 0 {
		fmt.Println("\nActivity")
		for _, e := range f.Events {
			fmt.Printf("  %s %-10s %-9s %s\n", hhmm(e.At), e.Agent, e.Kind, e.Detail)
		}
	}
	if len(c.Omitted) > 0 {
		fmt.Printf("\n(empty or missing: %s)\n", strings.Join(c.Omitted, ", "))
	}
}

func filledAny(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case float64:
		return t != 0
	case []any:
		for _, x := range t {
			if filledAny(x) {
				return true
			}
		}
		return false
	case map[string]any:
		for _, x := range t {
			if filledAny(x) {
				return true
			}
		}
		return false
	}
	return true
}

func showCard(c verbs.Ctx, key string) error {
	f, err := verbs.CardShow(c, verbs.CardKeyIn{Key: key})
	if err != nil {
		return err
	}
	if emit(f) {
		return nil
	}
	printCardFull(f)
	return nil
}

var cardShapesCmd = &cobra.Command{
	Use:   "shapes",
	Short: "The card catalog: types, sections, sizes, priorities",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardShapes(c, verbs.CardShapesIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Println(out.Rule)
		fmt.Println("\nTypes")
		for _, t := range out.Types {
			fmt.Printf("  %-7s needs %-10s offered %-16s %s\n", t.Type, t.TopLine, strings.Join(t.Offered, ","), t.Example)
		}
		fmt.Println("\nSections")
		for _, s := range out.Sections {
			f := strings.Join(s.Fields, ", ")
			if s.Kind == "strings" {
				f = "list of lines"
			}
			fmt.Printf("  %-11s %s\n              %s\n", s.Key, f, s.Hint)
		}
		fmt.Printf("\nSizes %s   Priorities %s (P3 highest)   Links %s\n", strings.Join(out.Sizes, " "), strings.Join(out.Priorities, " "), strings.Join(out.LinkKinds, " "))
		return nil
	},
}

var cardNewCmd = &cobra.Command{
	Use:   "new <title>",
	Short: "Create a card (its type's section via flags or --set, plus --accept)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		secs, err := parseSections(cardSections)
		if err != nil {
			return err
		}
		if secs, err = parseSet(cardSet, secs); err != nil {
			return err
		}
		in := verbs.CardNewIn{Type: cardType, Title: strings.Join(args, " "), Acceptance: cardAcceptance, Sections: secs,
			Body: cardBody, Size: cardSize, Priority: cardPriority, Tags: cardTags, Assignee: cardAssignee, Parent: cardParent,
			DueAt: cardDue, Estimate: cardEstimate, Artifact: cardArtifact, Source: cardSource}
		if cardAsA != "" || cardIWant != "" || cardSoThat != "" {
			in.Story = &verbs.CardStory{AsA: cardAsA, IWant: cardIWant, SoThat: cardSoThat}
		}
		out, err := verbs.CardNew(c, in)
		if err != nil {
			return err
		}
		if jsonOut {
			return showCard(c, out.Key)
		}
		if err := showCard(c, out.Key); err != nil {
			return err
		}
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
		out, err := verbs.CardList(c, verbs.CardListIn{State: cardListState, Type: cardListType, Tag: cardListTag,
			Assignee: cardListAssignee, Parent: cardListParent, Query: cardListQuery, All: cardListAll})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Cards) == 0 {
			fmt.Printf("no cards match (prefix %s). gears card new ... to add one\n", out.Prefix)
			return nil
		}
		for _, s := range out.States {
			if cardListState != "" && !strings.EqualFold(s.Name, cardListState) {
				continue
			}
			if s.Terminal && !cardListAll && cardListState == "" {
				continue
			}
			wip := ""
			if s.WIPLimit > 0 {
				wip = fmt.Sprintf("  wip %d", s.WIPLimit)
			}
			fmt.Printf("\n%s (%d)%s\n", s.Name, s.Count, wip)
			for _, cd := range out.Cards {
				if cd.State == s.Name {
					fmt.Print("  ")
					printCardLine(cd)
				}
			}
		}
		if len(out.Tags) > 0 {
			var parts []string
			for t, n := range out.Tags {
				parts = append(parts, fmt.Sprintf("%s %d", t, n))
			}
			fmt.Printf("\ntags: %s\n", strings.Join(parts, ", "))
		}
		return nil
	},
}

var cardShowCmd = &cobra.Command{
	Use:   "show <key>",
	Short: "The whole card: sections, brief, comments, links, checklists, activity",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		return showCard(c, args[0])
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
		secs, err := parseSections(cardSections)
		if err != nil {
			return err
		}
		if secs, err = parseSet(cardSet, secs); err != nil {
			return err
		}
		in := verbs.CardUpdateIn{Key: args[0], Sections: secs}
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
		if f.Changed("size") {
			in.Size = &cardSize
		}
		if f.Changed("priority") {
			in.Priority = &cardPriority
		}
		if f.Changed("assignee") {
			in.Assignee = &cardAssignee
		}
		if f.Changed("parent") {
			in.Parent = &cardParent
		}
		if f.Changed("due") {
			in.DueAt = &cardDue
		}
		if f.Changed("estimate") {
			in.Estimate = &cardEstimate
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
		if _, err := verbs.CardUpdate(c, in); err != nil {
			return err
		}
		return showCard(c, args[0])
	},
}

var cardMoveCmd = &cobra.Command{
	Use:   "move <key> [state]",
	Short: "Move a card to a state (and --pos top|bottom|after:<key>)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardMove(c, verbs.CardMoveIn{Key: args[0], State: strings.Join(args[1:], " "), Position: cardMovePos})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printCardLine(out.Card)
		fmt.Printf("         now in %s\n", out.Card.State)
		if out.Warning != "" {
			fmt.Println("  !", out.Warning)
		}
		return nil
	},
}

var cardClaimCmd = &cobra.Command{
	Use:   "claim <key>",
	Short: "I'm on it: assign to me and move out of the default state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardClaim(c, verbs.CardKeyIn{Key: args[0]})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printCardLine(out.Card)
		fmt.Printf("         now in %s\n", out.Card.State)
		if out.Warning != "" {
			fmt.Println("  !", out.Warning)
		}
		return nil
	},
}

var cardCommentCmd = &cobra.Command{
	Use:   "comment <key> <text>",
	Short: "Add evidence or a note to a card",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardCommentAdd(c, verbs.CardCommentIn{Key: args[0], Body: strings.Join(args[1:], " ")})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("%s %s: %s\n", hhmm(out.CreatedAt), out.Agent, out.Body)
		return nil
	},
}

var cardTagCmd = &cobra.Command{
	Use:   "tag <key> [+tag|-tag|tag ...]",
	Short: "Add (bare or +) and remove (-) tags",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		var add, remove []string
		for _, t := range args[1:] {
			if strings.HasPrefix(t, "-") {
				remove = append(remove, t[1:])
			} else {
				add = append(add, strings.TrimPrefix(t, "+"))
			}
		}
		out, err := verbs.CardTag(c, verbs.CardTagIn{Key: args[0], Add: add, Remove: remove})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		printCardLine(*out)
		return nil
	},
}

var cardLinkCmd = &cobra.Command{
	Use:   "link <key> [target-key]",
	Short: "Link cards: --kind blocks|blocked-by|related, or --kind ref --url",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.CardLinkIn{Key: args[0], Kind: cardLinkKind, URL: cardLinkURL, Title: cardLinkTtl}
		if cardLinkRm != "" {
			in.Op, in.Link = "remove", cardLinkRm
		} else if len(args) > 1 {
			in.Target = args[1]
		}
		out, err := verbs.CardLinkOp(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		for _, l := range out.Links {
			fmt.Printf("  %s %s %s%s %s\n", short(l.ID), l.Kind, l.Target, l.URL, l.Title)
		}
		return nil
	},
}

var cardCheckCmd = &cobra.Command{
	Use:   "check <key> [items...]",
	Short: "Checklists: --op add|item|tick|untick|remove (default: add with items, or tick an item)",
	Long: `  gears card check GRS-3 "Button shows" "Revoked user refused"     # add UAT with two items
  gears card check GRS-3 --op tick 1                                  # tick item 1
  gears card check GRS-3 --op item "Logout clears session"            # append an item
  gears card check GRS-3 --op remove --list UAT`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.CardChecklistIn{Key: args[0], Op: cardCheckOp, Checklist: cardCheckLst}
		switch cardCheckOp {
		case "add":
			in.Items = args[1:]
		default:
			in.Item = strings.Join(args[1:], " ")
		}
		out, err := verbs.CardChecklistOp(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		for _, cl := range out.Checklists {
			fmt.Printf("%s (%d/%d)\n", cl.Title, cl.Done, cl.Total)
			for i, it := range cl.Items {
				mark := "[ ]"
				if it.Done {
					mark = "[x]"
				}
				fmt.Printf("  %d. %s %s\n", i+1, mark, it.Title)
			}
		}
		return nil
	},
}

var cardStatesCmd = &cobra.Command{
	Use:   "states [list|add <name>|rename <name> <new>|reorder <names...>|remove <name>|default <name>|terminal <name>|wip <name> <n>]",
	Short: "Manage the swimlanes",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		in := verbs.CardStatesIn{Op: "list"}
		if len(args) > 0 {
			in.Op = args[0]
		}
		rest := args
		if len(rest) > 0 {
			rest = rest[1:]
		}
		switch in.Op {
		case "add":
			if len(rest) < 1 {
				return fmt.Errorf("states add <name>")
			}
			in.Name = strings.Join(rest, " ")
			if cmd.Flags().Changed("wip") {
				in.WIPLimit = &cardStateWIP
			}
		case "rename":
			if len(rest) < 2 {
				return fmt.Errorf("states rename <name> <new>")
			}
			in.Name, in.NewName = rest[0], strings.Join(rest[1:], " ")
		case "reorder":
			in.Order = rest
		case "remove":
			in.Name = strings.Join(rest, " ")
		case "default":
			t := true
			in.Op, in.Name, in.Default = "set", strings.Join(rest, " "), &t
		case "terminal":
			t := true
			in.Op, in.Name, in.Terminal = "set", strings.Join(rest, " "), &t
		case "wip":
			if len(rest) < 2 {
				return fmt.Errorf("states wip <name> <n>")
			}
			var n int
			fmt.Sscanf(rest[len(rest)-1], "%d", &n)
			in.Op, in.Name, in.WIPLimit = "set", strings.Join(rest[:len(rest)-1], " "), &n
		}
		out, err := verbs.CardStates(c, in)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		for i, s := range out.States {
			flags := ""
			if s.Default {
				flags += " default"
			}
			if s.Terminal {
				flags += " terminal"
			}
			if s.WIPLimit > 0 {
				flags += fmt.Sprintf(" wip %d", s.WIPLimit)
			}
			fmt.Printf("  %d. %-14s %3d%s\n", i+1, s.Name, s.Count, flags)
		}
		return nil
	},
}

var cardActivityCmd = &cobra.Command{
	Use:   "activity",
	Short: "Recent events across the board",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.CardActivity(c, verbs.CardActivityIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		for _, e := range out.Events {
			fmt.Printf("%s %-8s %-10s %-9s %s\n", hhmm(e.At), e.Card, e.Agent, e.Kind, e.Detail)
		}
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
		fmt.Fprintf(os.Stdout, "deleted %s  %s\n", out.Key, out.Title)
		return nil
	},
}

func init() {
	shapeFlags := func(cmd *cobra.Command) {
		cmd.Flags().StringVar(&cardAsA, "as-a", "", "story: who wants this")
		cmd.Flags().StringVar(&cardIWant, "i-want", "", "story: what they want to do")
		cmd.Flags().StringVar(&cardSoThat, "so-that", "", "story: why it matters")
		cmd.Flags().StringArrayVar(&cardAcceptance, "accept", nil, "a done-when line (repeatable); write 'X, proven by Y'")
		cmd.Flags().StringArrayVar(&cardSet, "set", nil, "section.field=value (repeatable), e.g. defect.symptom=..., brief.files=a.go,b.go")
		cmd.Flags().StringVar(&cardSections, "sections", "", "sections as a JSON object")
		cmd.Flags().StringVar(&cardSize, "size", "", "S | M | L")
		cmd.Flags().StringVar(&cardPriority, "priority", "", "P0 | P1 | P2 | P3 (P3 highest)")
		cmd.Flags().StringVar(&cardAssignee, "assignee", "", "agent label")
		cmd.Flags().StringVar(&cardParent, "parent", "", "parent card key")
		cmd.Flags().StringVar(&cardDue, "due", "", "YYYY-MM-DD")
		cmd.Flags().IntVar(&cardEstimate, "estimate", 0, "minutes")
	}
	shapeFlags(cardNewCmd)
	cardNewCmd.Flags().StringVarP(&cardType, "type", "t", "story", "story | defect | task | spike | chore | epic")
	cardNewCmd.Flags().StringVar(&cardBody, "body", "", "original text; evidence goes in comments")
	cardNewCmd.Flags().StringArrayVar(&cardTags, "tag", nil, "tag (repeatable)")
	cardNewCmd.Flags().StringVar(&cardArtifact, "artifact", "", "artifact file this card points at")
	cardNewCmd.Flags().StringVar(&cardSource, "source", "agent", "agent | user: who shaped the card")

	cardListCmd.Flags().StringVar(&cardListState, "state", "", "only this state")
	cardListCmd.Flags().StringVar(&cardListType, "type", "", "only this type")
	cardListCmd.Flags().StringVar(&cardListTag, "tag", "", "only this tag")
	cardListCmd.Flags().StringVar(&cardListAssignee, "assignee", "", "only this agent (me = you)")
	cardListCmd.Flags().StringVar(&cardListParent, "parent", "", "only children of this card")
	cardListCmd.Flags().StringVarP(&cardListQuery, "q", "q", "", "substring search")
	cardListCmd.Flags().BoolVar(&cardListAll, "all", false, "include completed cards")

	shapeFlags(cardUpdateCmd)
	cardUpdateCmd.Flags().StringVar(&cardSetTitle, "title", "", "new title")
	cardUpdateCmd.Flags().StringVar(&cardSetBody, "body", "", "new body")
	cardUpdateCmd.Flags().StringVar(&cardSetType, "type", "", "story | defect | task | spike | chore | epic")
	cardUpdateCmd.Flags().StringVar(&cardSetArt, "artifact", "", "artifact file; empty clears")
	cardUpdateCmd.Flags().StringVar(&cardSetExt, "external", "", "Jira or Kanboard key; empty clears")

	cardMoveCmd.Flags().StringVar(&cardMovePos, "pos", "", "top | bottom | after:<key>")

	cardLinkCmd.Flags().StringVar(&cardLinkKind, "kind", "related", "blocks | blocked-by | related | ref")
	cardLinkCmd.Flags().StringVar(&cardLinkURL, "url", "", "for ref")
	cardLinkCmd.Flags().StringVar(&cardLinkTtl, "title", "", "for ref")
	cardLinkCmd.Flags().StringVar(&cardLinkRm, "remove", "", "link id to remove")

	cardCheckCmd.Flags().StringVar(&cardCheckOp, "op", "add", "add | item | tick | untick | remove")
	cardCheckCmd.Flags().StringVar(&cardCheckLst, "list", "", "checklist title (default UAT, or the only one)")

	cardStatesCmd.Flags().IntVar(&cardStateWIP, "wip", 0, "WIP limit for add")

	cardCmd.AddCommand(cardShapesCmd, cardNewCmd, cardListCmd, cardShowCmd, cardUpdateCmd, cardMoveCmd, cardClaimCmd,
		cardCommentCmd, cardTagCmd, cardLinkCmd, cardCheckCmd, cardStatesCmd, cardActivityCmd, cardDeleteCmd)
	root.AddCommand(cardCmd)
}
