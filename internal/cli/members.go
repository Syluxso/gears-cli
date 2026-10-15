package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// Members and invites (GRS-42). These read and write gears-api directly;
// there is no local copy, so they need gears login and a reachable API.

var (
	membersAll bool
	inviteRole string
)

var membersCmd = &cobra.Command{
	Use:   "members",
	Short: "Who is on this workspace, and its pending invites",
	Long: `The workspace's members (owners first) and its pending invites, from
gears-api. Invite an existing Gears account with gears invite <email>.

  gears members
  gears members --all        include removed members`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.MembersList(c, verbs.MembersListIn{All: membersAll})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		for _, m := range out.Members {
			fmt.Printf("%s  %-6s %-7s %s <%s>\n", m.UserID, m.Role, m.Status, orDash(m.Name), m.Email)
		}
		if len(out.Members) == 0 {
			fmt.Println("no members")
		}
		if len(out.Invites) > 0 {
			fmt.Println()
			fmt.Println("pending invites:")
			printInvites(out.Invites, false)
		}
		return nil
	},
}

var inviteCmd = &cobra.Command{
	Use:   "invite <email>",
	Short: "Invite an existing Gears account into this workspace",
	Long: `Invites an existing Gears account, by email, into this workspace. They
see it on their Home in the app (or gears invites) and accept it there.
Only an owner may invite an owner.

  gears invite bob@example.com
  gears invite bob@example.com --role owner
  gears invite revoke <id>     withdraw a pending invite (gears members lists them)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		iv, err := verbs.MemberInvite(c, verbs.MemberInviteIn{Email: args[0], Role: inviteRole})
		if err != nil {
			return err
		}
		if emit(iv) {
			return nil
		}
		fmt.Printf("invited %s as %s (invite %s)\n", orDash(iv.InviteeEmail), iv.Role, iv.ID)
		return nil
	},
}

var inviteRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Withdraw a pending invite of this workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		iv, err := verbs.InviteRevoke(c, verbs.InviteRevokeIn{ID: args[0]})
		if err != nil {
			return err
		}
		if emit(iv) {
			return nil
		}
		fmt.Printf("revoked invite %s for %s\n", iv.ID, orDash(iv.InviteeEmail))
		return nil
	},
}

var invitesCmd = &cobra.Command{
	Use:   "invites",
	Short: "Invites sent to me: accept or decline them",
	Long: `The pending invites sent to the logged-in account, for any workspace.

  gears invites
  gears invites accept <id>
  gears invites decline <id>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.InviteList(c, verbs.InviteListIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if len(out.Invites) == 0 {
			fmt.Println("no pending invites")
			return nil
		}
		printInvites(out.Invites, true)
		return nil
	},
}

func respondCmd(use, short string, accept bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ctx()
			if err != nil {
				return err
			}
			in := verbs.InviteRespondIn{ID: args[0]}
			var out *verbs.InviteRespondOut
			if accept {
				out, err = verbs.InviteAccept(c, in)
			} else {
				out, err = verbs.InviteDecline(c, in)
			}
			if err != nil {
				return err
			}
			if emit(out) {
				return nil
			}
			fmt.Printf("%s invite %s to %s\n", out.Invite.Status, out.Invite.ID, wsName(out.Invite))
			if out.Note != "" {
				fmt.Println(out.Note)
			}
			return nil
		},
	}
}

func wsName(iv api.Invite) string {
	if iv.WorkspaceName != "" {
		return iv.WorkspaceName
	}
	if iv.WorkspaceSlug != "" {
		return iv.WorkspaceSlug
	}
	return iv.WorkspaceID
}

// printInvites lists invites: to me (mine, by workspace) or of this
// workspace (by invitee).
func printInvites(ivs []api.Invite, mine bool) {
	for _, iv := range ivs {
		who := orDash(iv.InviteeEmail)
		if mine {
			who = wsName(iv)
		}
		fmt.Printf("%s  %-6s %s  %s\n", iv.ID, iv.Role, who, iv.CreatedAt.Local().Format("Jan 2 15:04"))
	}
}

func init() {
	membersCmd.Flags().BoolVar(&membersAll, "all", false, "include removed members")
	inviteCmd.Flags().StringVar(&inviteRole, "role", "member", "member or owner (owners only)")
	inviteCmd.AddCommand(inviteRevokeCmd)
	invitesCmd.AddCommand(respondCmd("accept", "Accept an invite sent to me", true), respondCmd("decline", "Decline an invite sent to me", false))
	root.AddCommand(membersCmd, inviteCmd, invitesCmd)
}
