// Package cli wires the cobra commands. Each command is thin: it resolves the
// project, calls into an internal package, and prints. Logic lives below.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set by the build (-ldflags "-X .../cli.Version=v3.0.0").
var Version = "v3.0.0-dev"

var root = &cobra.Command{
	Use:           "gears",
	Short:         "Local tools for agents working on a Gears project",
	Version:       Version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command and prints any error on stderr.
func Execute() error {
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return err
	}
	return nil
}
