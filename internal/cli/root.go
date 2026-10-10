// Package cli wires the cobra commands. Each command is a thin adapter: it
// builds a verbs input from flags, calls the verb, and renders the result
// as text or, with --json, as the same JSON the MCP tools return.
package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

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
	root.PersistentFlags().StringVarP(&globalAgent, "agent", "a", "", "act as this agent (default: $GEARS_AGENT or \"local\")")
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

// ctx resolves the project from the working directory and the acting agent.
func ctx() (verbs.Ctx, error) {
	p, err := project.Find(mustCwd())
	if err != nil {
		return verbs.Ctx{}, err
	}
	agent := globalAgent
	if agent == "" {
		agent = session.DefaultAgent()
	}
	return verbs.Ctx{Project: p, Agent: agent}, nil
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
