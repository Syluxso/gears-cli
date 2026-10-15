// Package cli wires the cobra commands. Each command is a thin adapter: it
// builds a verbs input from flags, calls the verb, and renders the result
// as text or, with --json, as the same JSON the MCP tools return.
package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// Version is set by the build (-ldflags "-X .../cli.Version=v3.0.0").
var Version = "v3.0.0-dev"

var (
	jsonOut     bool
	globalAgent string
)

var root = &cobra.Command{
	Use:           "gears",
	Short:         "Local tools for agents working on a Gears project",
	Version:       Version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "print the result as JSON (the same shape the MCP tools return)")
	root.PersistentFlags().StringVarP(&globalAgent, "agent", "a", "", "act as this agent (default: $GEARS_AGENT, else the name gears init saved for this workspace, else the gears login --label, else \"local\")")
	session.LoginLabel = loginLabelFor
	session.WorkspaceAgent = workspaceAgentAt
}

// workspaceAgentAt is the agent name gears init saved for the workspace dir
// is in ("" is the working directory), the third place the acting agent
// comes from (GRS-63).
func workspaceAgentAt(dir string) string {
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return ""
		}
	}
	p, err := project.Find(dir)
	if err != nil {
		return ""
	}
	return p.AgentName()
}

// loginLabelFor is the label gears login saved for the current API host,
// the fourth place the acting agent comes from (GRS-21).
func loginLabelFor() string {
	creds, err := api.LoadCredentials()
	if err != nil {
		return ""
	}
	return creds.Hosts[api.Host()].Label
}

// Execute runs the root command and prints any error on stderr.
func Execute() error {
	if err := root.Execute(); err != nil {
		if jsonOut {
			b, _ := json.Marshal(map[string]string{"error": err.Error()})
			fmt.Fprintln(os.Stderr, string(b))
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return err
	}
	return nil
}

// ctx resolves the binding from the working directory (walking up to the
// workspace root), the project the directory is in, and the acting agent.
func ctx() (verbs.Ctx, error) {
	return ctxAt(mustCwd())
}

func ctxAt(dir string) (verbs.Ctx, error) {
	p, err := project.Find(dir)
	if err != nil {
		return verbs.Ctx{}, err
	}
	agent, source := session.ResolveAgentAt(globalAgent, dir)
	return verbs.Ctx{Project: p, Agent: agent, AgentSource: source, Current: project.Current(p, dir), Notes: os.Stderr}, nil
}

// emit prints v as JSON when --json is set and returns true; otherwise it
// returns false so the caller renders text.
func emit(v any) bool {
	if !jsonOut {
		return false
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return true
	}
	fmt.Println(string(b))
	return true
}

func mustCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return dir
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// short trims an id for display.
func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// tail is the short form for UUID v7 ids: the last 8 hex digits, which are
// random. The leading digits are a timestamp shared by ids minted together.
func tail(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

// hhmm renders an RFC3339 timestamp as local HH:MM.
func hhmm(rfc string) string {
	if rfc == "" {
		return ""
	}
	t := parseTS(rfc)
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("15:04")
}
