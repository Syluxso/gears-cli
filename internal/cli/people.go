package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

// gears people (GRS-36): who a desk item can be for.
var peopleCmd = &cobra.Command{
	Use:   "people",
	Short: "The workspace's people: ids for gears desk send --assign",
	Long: `The workspace's members from gears-api, with the agent labels their
devices use. Cached locally; offline the cached list is shown, marked stale.

  gears people
  gears desk send --type work --assign <id> "hello world"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.PeopleList(c, verbs.PeopleListIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.Stale {
			fmt.Printf("stale (cached %s): %s\n", orDash(out.FetchedAt), out.Note)
		}
		for _, p := range out.People {
			me := ""
			if p.ID == out.Me {
				me = "  (you)"
			}
			fmt.Printf("%s  %-6s %s <%s>%s\n", p.ID, p.Role, orDash(p.Name), p.Email, me)
			if len(p.Labels) > 0 {
				fmt.Printf("          agents: %s\n", strings.Join(p.Labels, ", "))
			}
		}
		if len(out.People) == 0 {
			fmt.Println("no people known yet")
		}
		return nil
	},
}

func init() { root.AddCommand(peopleCmd) }
