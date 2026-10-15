package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// archCmd shows the workspace's architecture artifact (GRS-41): the master
// design doc, arch--primary.md, that gears status always carries in full.
var archCmd = &cobra.Command{
	Use:     "arch",
	Aliases: []string{"architecture"},
	Short:   "Show the workspace's architecture doc (arch--primary.md)",
	Long: `The workspace's master design doc: .gears/artifacts/arch--primary.md.
Free-form for now (no required sections or length), one per workspace, and
gears status (text, --json and the MCP status tool) always shows it in full.
It syncs like any other artifact.

  gears arch          print it
  gears arch edit     open it in $VISUAL / $EDITOR, then reconcile
  gears arch init     write the stub if the workspace has none`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.Arch(c, verbs.ArchIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.Missing {
			fmt.Println(out.Hint)
			return nil
		}
		fmt.Print(out.Body)
		if !strings.HasSuffix(out.Body, "\n") {
			fmt.Println()
		}
		return nil
	},
}

var archInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Write the stub arch--primary.md if the workspace has none",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.ArchInit(c, verbs.ArchInitIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.Created {
			fmt.Printf("wrote %s\nFill it in: gears arch edit\n", out.Path)
		} else {
			fmt.Printf("kept %s (already there; init never overwrites it)\n", out.Path)
		}
		return nil
	},
}

var archEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open arch--primary.md in $VISUAL / $EDITOR, then reconcile",
	Long: `Open the architecture doc in $VISUAL, else $EDITOR, else notepad on
Windows and vi elsewhere. When the editor exits the row is reconciled with
the file, so the next sync carries the change.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		path, _, ok, err := artifact.Arch(c.Project)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no architecture doc yet: gears arch init writes the stub " + artifact.ArchFile)
		}
		name, eargs := editorCommand()
		ed := exec.Command(name, append(eargs, path)...)
		ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := ed.Run(); err != nil {
			return fmt.Errorf("editor %s: %w", name, err)
		}
		changes, err := artifact.Reconcile(c.Project)
		if err != nil {
			return err
		}
		for _, ch := range changes {
			fmt.Printf("%-11s %s  %s\n", ch.Action, ch.File, ch.Detail)
		}
		if len(changes) == 0 {
			fmt.Println("no change")
		}
		return nil
	},
}

// editorCommand is $VISUAL, else $EDITOR, else the platform default. The
// variable may carry arguments ("code --wait").
func editorCommand() (string, []string) {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(v)); len(f) > 0 {
			return f[0], f[1:]
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad", nil
	}
	return "vi", nil
}

func init() {
	archCmd.AddCommand(archInitCmd, archEditCmd)
	root.AddCommand(archCmd)
}
