package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears/internal/mcpserver"
	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/session"
	"github.com/Syluxso/gears/internal/verbs"
)

var runDir string

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Serve the local MCP over stdio",
	Long: `The agent's door. Serves every Gears verb as an MCP tool over stdin and
stdout for the project in the current directory (or --dir). Sync to the
API arrives here later; today this is the stdio server only.

Harness config (Claude Code, .mcp.json):
  { "mcpServers": { "gears": { "command": "gears", "args": ["run", "--dir", "/path/to/repo"] } } }

Set GEARS_AGENT in the harness env so tools act as that agent.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := runDir
		if dir == "" {
			dir = mustCwd()
		}
		p, err := project.Find(dir)
		if err != nil {
			return err
		}
		agent := globalAgent
		if agent == "" {
			agent = session.DefaultAgent()
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fmt.Fprintf(os.Stderr, "gears mcp: project %s, agent %s, %s\n", p.Slug, agent, p.Root)
		s := mcpserver.New(verbs.Ctx{Project: p, Agent: agent}, Version)
		return mcpserver.ServeStdio(ctx, s)
	},
}

func init() {
	runCmd.Flags().StringVar(&runDir, "dir", "", "project directory (default: current directory)")
	root.AddCommand(runCmd)
}
