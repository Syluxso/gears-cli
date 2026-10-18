package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/hooks"
	"github.com/Syluxso/gears-cli/internal/project"
)

// gears hooks (GRS-93): the Claude Code Stop hook that keeps an agent on
// the run its label owns (gears runbook next --hook), written into
// <workspace>/.claude/settings.json.

var hooksDir string

const hooksLong = `The Stop hook runs gears runbook next --hook each time Claude Code is about
to stop. Only when a running run this agent label OWNS waits on the agent
does it block the stop, with the step's instruction, so the agent goes on
to the next step without a nudge. A step for a person (approval, gate, an
unanswered ask) or the system, a finished run, no run, an error, gears-api
offline or a slow answer (over ~2s) let the agent stop. When the agent was
already continuing because of the hook and the run has not moved, it stops
too (it could not make progress). Claude Code allows at most 8 blocks in a
row without a tool call.

To stop a looping agent: press Esc, tell it to stop (it ends by telling
you why), stop or claim the run (gears runbook stop / claim), or gears
hooks uninstall.

The hook is written into <dir>/.claude/settings.json (default dir: the
workspace root), merged: other settings and other Stop hooks are kept.
With -a <label> the command carries that label; else, when the gears MCP
server in <dir>/.mcp.json sets GEARS_AGENT, it carries that one (Claude Code
gives that env to the MCP server only, not to hooks); otherwise the hook acts
as the label every command resolves ($GEARS_AGENT, the workspace's agent
name, the login label). Run install again after changing the label.`

func hooksTarget() (string, error) {
	if d := strings.TrimSpace(hooksDir); d != "" {
		abs, err := filepath.Abs(d)
		if err != nil {
			return "", err
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return "", fmt.Errorf("--dir %s is not a directory", d)
		}
		return abs, nil
	}
	p, err := project.Find(mustCwd())
	if err != nil {
		return "", fmt.Errorf("%v (or pass --dir)", err)
	}
	return p.Root, nil
}

func printHookStatus(st hooks.Status) {
	if st.Installed {
		fmt.Printf("Stop hook installed in %s\n  command: %s\n", st.Path, st.Command)
		return
	}
	fmt.Printf("no Gears Stop hook in %s\n", st.Path)
}

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "The Claude Code Stop hook that keeps an agent on its run: install, uninstall, status",
	Long:  hooksLong,
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Write the Gears Stop hook into <workspace>/.claude/settings.json (merged, idempotent)",
	Long:  hooksLong,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := hooksTarget()
		if err != nil {
			return err
		}
		st, err := hooks.Install(dir, hooks.Command(hooks.Agent(globalAgent, dir)))
		if err != nil {
			return err
		}
		if emit(st) {
			return nil
		}
		if st.Changed {
			fmt.Printf("wrote the Stop hook into %s\n  command: %s\n", st.Path, st.Command)
		} else {
			fmt.Printf("Stop hook already in %s\n  command: %s\n", st.Path, st.Command)
		}
		fmt.Println("It keeps the agent going only for runs its label owns; Esc or gears hooks uninstall stops it.")
		return nil
	},
}

var hooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the Gears Stop hook from <workspace>/.claude/settings.json (other settings stay)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := hooksTarget()
		if err != nil {
			return err
		}
		st, err := hooks.Uninstall(dir)
		if err != nil {
			return err
		}
		if emit(st) {
			return nil
		}
		if st.Removed {
			fmt.Printf("removed the Stop hook from %s\n", st.Path)
		} else {
			fmt.Printf("no Gears Stop hook in %s\n", st.Path)
		}
		return nil
	},
}

var hooksStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Whether the Gears Stop hook is in <workspace>/.claude/settings.json",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := hooksTarget()
		if err != nil {
			return err
		}
		st, err := hooks.Read(dir)
		if err != nil {
			return err
		}
		if emit(st) {
			return nil
		}
		printHookStatus(st)
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{hooksInstallCmd, hooksUninstallCmd, hooksStatusCmd} {
		c.Flags().StringVar(&hooksDir, "dir", "", "directory whose .claude/settings.json holds the hook (default: the workspace root)")
	}
	hooksCmd.AddCommand(hooksInstallCmd, hooksUninstallCmd, hooksStatusCmd)
	root.AddCommand(hooksCmd)
}
