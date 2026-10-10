package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// hydrateCmd is a compatibility stub. Gears v2 workspaces tell their agents
// to run "gears hydrate --chat" at the start of every session; v3 has no
// inbox to hydrate from. The command exists so those sessions do not open
// with an error, and it points at what replaced it. Hidden from help.
// Remove once no v2 workspace is still in use.
var hydrateCmd = &cobra.Command{
	Use:                "hydrate",
	Hidden:             true,
	DisableFlagParsing: true, // accept --chat and anything else v2 passed
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Gears v3 has no inbox. In a bound repo run: gears status")
		return nil
	},
}

func init() {
	root.AddCommand(hydrateCmd)
}
