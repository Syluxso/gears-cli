package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/mcpserver"
	"github.com/Syluxso/gears-cli/internal/sync"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var runDir string

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Serve the local MCP over stdio",
	Long: `The agent's door. Serves every Gears verb as an MCP tool over stdin and
stdout for the project in the current directory (or --dir).

Cards sync immediately (GRS-51): while it runs, the server holds the
workspace's live board open (when logged in) and applies every change made
elsewhere (the app, another machine) within a moment. Card writes push at
once. Desk, sessions and artifacts still move with sync. GEARS_CARD_SYNC=off
turns the card behaviour off. Logs go to stderr; stdout is the MCP channel.

Harness config (Claude Code, .mcp.json):
  { "mcpServers": { "gears": { "command": "gears", "args": ["run", "--dir", "/path/to/repo"] } } }

Set GEARS_AGENT in the harness env so tools act as that agent.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := runDir
		if dir == "" {
			dir = mustCwd()
		}
		c, err := ctxAt(dir)
		if err != nil {
			return err
		}
		c.Notes = nil // notes ride in the tool results; stdout is the MCP channel
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fmt.Fprintf(os.Stderr, "gears mcp: project %s, agent %s, %s\n", c.Project.Slug, c.Agent, c.Project.Root)
		go liveBoard(ctx, c, nil)
		s := mcpserver.New(c, Version)
		return mcpserver.ServeStdio(ctx, s)
	},
}

// liveBoard holds the workspace's board stream open until ctx ends (GRS-51).
// It never fails its caller: no login, no binding, an API without the live
// board are each one line on stderr, and cards still sync on every write
// and read. onChange receives applied changes (gears watch prints them).
func liveBoard(ctx context.Context, c verbs.Ctx, onChange func(sync.CardChange)) error {
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "gears live: "+format+"\n", args...)
	}
	if sync.Disabled() {
		logf("card sync is off (GEARS_CARD_SYNC)")
		return errLiveOff
	}
	host := api.Host()
	cl, err := api.Open(host)
	if err != nil {
		logf("%s; the board updates on gears sync", err)
		return err
	}
	if !sync.CardsReady(c.Project) {
		logf("cards have not synced here yet; run gears sync once, then the board stays live")
		return sync.ErrCardsNotReady
	}
	return sync.Live(ctx, sync.LiveOptions{Client: cl, Project: c.Project, Logf: logf, OnChange: onChange})
}

var errLiveOff = errors.New("card sync is off (GEARS_CARD_SYNC)")

func init() {
	runCmd.Flags().StringVar(&runDir, "dir", "", "project directory (default: current directory)")
	root.AddCommand(runCmd)
}
