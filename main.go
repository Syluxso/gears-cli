// gears is the local door to a Gears project: it binds a directory to a
// project, keeps shaped artifacts beside the code, and records the session
// log. Everything in this binary that needs an account is not here yet.
package main

import (
	"os"

	"github.com/Syluxso/gears-cli/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
