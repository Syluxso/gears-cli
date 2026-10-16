package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

// Workspace soft delete and restore (GRS-80). They live on gears-api and
// work on a workspace marked deleted here (ctxAny).

var (
	wsListDeleted bool
	wsDeleteYes   bool
	wsConfirm     string
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "List, delete (soft) and restore workspaces on gears-api",
	Long: `Workspaces on gears-api. Deleting is soft and for owners only: nothing is
erased, everything in the workspace is unreachable until an owner restores
it. This folder's cache keeps its data and refuses reads meanwhile.

  gears workspace list               your workspaces
  gears workspace list --deleted     your deleted ones (the ones you own)
  gears workspace delete             this workspace: type its slug to confirm
  gears workspace delete <id|slug> --confirm <slug>     non-interactive
  gears workspace restore [<id|slug>]                   default this workspace`,
}

// wsCtx is the binding when there is one; the workspace commands also run
// outside a workspace.
func wsCtx() verbs.Ctx {
	c, err := ctxAny()
	if err != nil {
		return verbs.Ctx{}
	}
	return c
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "Your workspaces, or with --deleted your deleted ones",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out, err := verbs.WorkspaceList(wsCtx(), verbs.WorkspaceListIn{Deleted: wsListDeleted})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if wsListDeleted {
			for _, d := range out.Deleted {
				fmt.Printf("%s  %-24s deleted %s by %s\n", d.ID, d.Slug, d.DeletedAt.Local().Format("2006-01-02 15:04"), orDash(d.DeletedBy))
			}
			if len(out.Deleted) == 0 {
				fmt.Println("no deleted workspaces")
			}
			return nil
		}
		for _, w := range out.Workspaces {
			here := ""
			if strings.EqualFold(w.ID, out.Here) {
				here = "  (here)"
			}
			fmt.Printf("%s  %-24s %s%s\n", w.ID, w.Slug, w.Prefix, here)
		}
		if len(out.Workspaces) == 0 {
			fmt.Println("no workspaces")
		}
		return nil
	},
}

var workspaceDeleteCmd = &cobra.Command{
	Use:   "delete [<id|slug>]",
	Short: "Soft delete a workspace on gears-api (owners only)",
	Long: `Soft deletes a workspace (default this one) on gears-api. Owners only.
Confirm by typing its slug at the prompt, or pass --confirm <slug>; --yes
confirms the workspace named (or this one) without a prompt.

Everything in it becomes unreachable for everyone: board, desk, artifacts,
sessions, runs (running ones stop), invites (pending ones are revoked).
Nothing is erased: gears workspace restore brings it back.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := wsCtx()
		key := ""
		if len(args) == 1 {
			key = args[0]
		}
		confirm := strings.TrimSpace(wsConfirm)
		if confirm == "" {
			slug := slugFor(c, key)
			switch {
			case wsDeleteYes && slug != "":
				confirm = slug
			case wsDeleteYes:
				return fmt.Errorf("--yes needs a workspace slug: name it (gears workspace delete <slug> --yes) or pass --confirm <slug>")
			case term.IsTerminal(int(os.Stdin.Fd())):
				what := slug
				if what == "" {
					what = key
				}
				fmt.Fprintf(os.Stderr, "This soft deletes workspace %s for everyone. Type its slug to confirm: ", what)
				confirm = prompt("")
			default:
				return fmt.Errorf("not a terminal: pass --confirm <slug> (or --yes)")
			}
		}
		out, err := verbs.WorkspaceDelete(c, verbs.WorkspaceDeleteIn{Workspace: key, Confirm: confirm})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("deleted workspace %s (%s)\n%s\n", out.Slug, short(out.Deleted), out.Note)
		return nil
	},
}

// slugFor is the slug the delete is about when it is known here without
// asking the API: this binding's, or key itself when it is not an id.
func slugFor(c verbs.Ctx, key string) string {
	if key == "" {
		if c.Project != nil && c.Project.IsWorkspace() {
			return c.Project.Slug
		}
		if c.Project != nil {
			if anc := c.Project.Ancestor(); anc != nil {
				return anc.Slug
			}
		}
		return ""
	}
	if len(key) == 36 && strings.Count(key, "-") == 4 {
		if c.Project != nil && strings.EqualFold(c.Project.WorkspaceID(), key) && c.Project.IsWorkspace() {
			return c.Project.Slug
		}
		return ""
	}
	return key
}

var workspaceRestoreCmd = &cobra.Command{
	Use:   "restore [<id|slug>]",
	Short: "Restore a deleted workspace you own (default this one)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := ""
		if len(args) == 1 {
			key = args[0]
		}
		out, err := verbs.WorkspaceRestore(wsCtx(), verbs.WorkspaceRestoreIn{Workspace: key})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("restored workspace %s (%s)\n", out.Workspace.Slug, short(out.Workspace.ID))
		if out.Note != "" {
			fmt.Println(out.Note)
		}
		return nil
	},
}

func init() {
	workspaceListCmd.Flags().BoolVar(&wsListDeleted, "deleted", false, "list your deleted workspaces (the ones you own)")
	workspaceDeleteCmd.Flags().BoolVar(&wsDeleteYes, "yes", false, "confirm without a prompt (the workspace named, or this one)")
	workspaceDeleteCmd.Flags().StringVar(&wsConfirm, "confirm", "", "the workspace slug, typed back (non-interactive)")
	workspaceCmd.AddCommand(workspaceListCmd, workspaceDeleteCmd, workspaceRestoreCmd)
	root.AddCommand(workspaceCmd)
}
