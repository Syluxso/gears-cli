package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/sync"
)

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Follow the board live: one line per card change, Ctrl-C to stop",
	Long: `Holds the workspace's live board open (GRS-51), the same stream gears run
keeps for agents, and applies each change to the local cache as it lands,
printing one line per change:

  07:12 GRS-3 moved → Testing (by browser)

Connection state goes to stderr. --json prints each change as a JSON line.
Needs gears login and one gears sync.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fmt.Fprintf(os.Stderr, "gears watch: %s board on %s (Ctrl-C to stop)\n", c.Project.Prefix, api.Host())
		err = liveBoard(ctx, c, func(ch sync.CardChange) {
			if jsonOut {
				b, _ := json.Marshal(ch)
				fmt.Println(string(b))
				return
			}
			fmt.Println(ch.String())
		})
		if ctx.Err() != nil {
			return nil
		}
		return err
	},
}

func init() {
	root.AddCommand(watchCmd)
}
