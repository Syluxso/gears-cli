package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

var exportOut string

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Write the local record to JSON",
	Long: `The local record (.gears/.gearbox/local.db) is the desk, board, sessions
and runs for this clone. It is not committed, so deleting the clone loses
it. export writes every table to JSON: to stdout, or to --out <file>.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.Export(c, verbs.ExportIn{Out: exportOut})
		if err != nil {
			return err
		}
		if exportOut == "" {
			b, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(b))
			return nil
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("wrote %s\n", out.File)
		for _, t := range verbs.Tables {
			fmt.Printf("  %-16s %d\n", t, out.Counts[t])
		}
		return nil
	},
}

func init() {
	exportCmd.Flags().StringVar(&exportOut, "out", "", "file to write (default: stdout)")
	root.AddCommand(exportCmd)
}
