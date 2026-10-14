package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/sync"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	loginHost     string
	loginEmail    string
	loginPassword string
	loginLabel    string
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Get a device token, register this workspace, report what there is to pull",
	Long: `Logs in to gears-api with your gearsos.com account and saves a device token
in ~/.gears/credentials.json. Then makes sure the workspace and this project
exist in your account (creating them if not) and says whether there is
anything to pull. Nothing else.

  gears login --email you@example.com            (password prompted)
  gears login --api http://127.0.0.1:8220 --label claude-code+local

--label is the name this install speaks as by default; it can be changed
per call and is not a gate.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		host := loginHost
		if host == "" {
			host = api.Host()
		}
		email := strings.TrimSpace(loginEmail)
		if email == "" {
			email = prompt("email: ")
		}
		pw := loginPassword
		if pw == "" {
			pw = promptSecret("password: ")
		}
		if email == "" || pw == "" {
			return fmt.Errorf("email and password required")
		}
		label := loginLabel
		if label == "" {
			label = globalAgent
		}
		if label == "" {
			label = session.DefaultAgent()
		}
		hostname, _ := os.Hostname()
		cr, err := api.Login(context.Background(), host, email, pw, label, hostname)
		if err != nil {
			return err
		}
		fmt.Printf("logged in to %s as %s (label %s), token until %s\n", host, email, cr.Label, cr.ExpiresAt.Local().Format("2006-01-02"))

		c, err := ctx()
		if err != nil {
			fmt.Println("no project here; run gears init, then gears login again to register it")
			return nil
		}
		cl, err := api.Open(host)
		if err != nil {
			return err
		}
		out, err := sync.Reconcile(context.Background(), cl, c.Project)
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		verb := "found"
		if out.WorkspaceNew {
			verb = "created"
		}
		fmt.Printf("workspace %s (%s) %s in your account\n", out.Workspace.Slug, short(out.Workspace.ID), verb)
		if out.Project != nil {
			verb = "found"
			if out.ProjectNew {
				verb = "created"
			}
			if out.BoundByRemote {
				verb = "bound by git remote"
			}
			fmt.Printf("project %s (%s) %s\n", out.Project.Slug, short(out.Project.ID), verb)
		}
		if out.Projects > 0 {
			fmt.Printf("%d project%s from .gears/projects.yaml checked, %d created\n", out.Projects, plural(out.Projects, "", "s"), out.ProjectsCreated)
		}
		for _, n := range out.Notes {
			fmt.Println("  note:", n)
		}
		if out.Pending > 0 {
			fmt.Printf("%d change%s to pull: gears sync\n", out.Pending, plural(out.Pending, "", "s"))
		} else {
			fmt.Println("nothing to pull")
		}
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Revoke this device's token and forget it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		host := loginHost
		if host == "" {
			host = api.Host()
		}
		revoked, err := api.Logout(context.Background(), host)
		if err != nil {
			return err
		}
		if revoked {
			fmt.Printf("logged out of %s; device revoked\n", host)
		} else {
			fmt.Printf("logged out of %s; token forgotten (server not reached, revoke it in the app if needed)\n", host)
		}
		return nil
	},
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Push local changes to gears-api, then pull and apply",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.Sync(c, verbs.SyncIn{Host: loginHost})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		fmt.Printf("%s  workspace %s\npushed %d (skipped %d, failed %d)  pulled %d, applied %d  cursor %d\n",
			out.Host, short(out.Workspace), out.Pushed, out.Skipped, out.Failed, out.Pulled, out.Applied, out.Cursor)
		if out.Projects > 0 {
			fmt.Printf("projects: %d checked, %d created\n", out.Projects, out.ProjectsCreated)
		}
		if out.Cards != "" {
			fmt.Printf("cards: %s", out.Cards)
			if out.CardsRenumbered > 0 {
				fmt.Printf(", %d renumbered by the API", out.CardsRenumbered)
			}
			fmt.Println()
		}
		if out.ContentUp > 0 || out.ContentDown > 0 {
			fmt.Printf("artifact content: %d up, %d down\n", out.ContentUp, out.ContentDown)
		}
		for _, n := range out.Notes {
			fmt.Println("  note:", n)
		}
		return nil
	},
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Which API, whether the token works, which workspace this syncs to",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx()
		if err != nil {
			return err
		}
		out, err := verbs.Whoami(c, verbs.WhoamiIn{})
		if err != nil {
			return err
		}
		if emit(out) {
			return nil
		}
		if out.LoggedIn {
			fmt.Printf("%s  logged in, label %s, tenant %s\n", out.Host, out.Label, short(out.TenantID))
		} else {
			fmt.Printf("%s  not logged in: %s\n", out.Host, out.Error)
		}
		fmt.Printf("workspace %s  project %s\n", short(out.Workspace), short(out.Project))
		printAgent(os.Stdout, out.Agent)
		return nil
	},
}

// stdin is shared by every prompt so a piped "email\npassword\n" is not
// swallowed by the first prompt's read buffer.
var stdin = bufio.NewReader(os.Stdin)

func prompt(label string) string {
	fmt.Fprint(os.Stderr, label)
	s, _ := stdin.ReadString('\n')
	return strings.TrimSpace(s)
}

// promptSecret reads a line without echo when stdin is a terminal, then
// prints the newline the terminal swallowed. Piped stdin is read as a plain
// line so scripts still work.
func promptSecret(label string) string {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return prompt(label)
	}
	fmt.Fprint(os.Stderr, label)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func init() {
	for _, c := range []*cobra.Command{loginCmd, logoutCmd, syncCmd} {
		c.Flags().StringVar(&loginHost, "api", "", "API host (default: $GEARS_API, else the saved default, else "+api.DefaultHost+")")
	}
	loginCmd.Flags().StringVar(&loginEmail, "email", "", "account email (prompted if empty)")
	loginCmd.Flags().StringVar(&loginPassword, "password", "", "account password (prompted if empty; prefer the prompt)")
	loginCmd.Flags().StringVar(&loginLabel, "label", "", "default label this install speaks as (default: --agent or $GEARS_AGENT)")
	root.AddCommand(loginCmd, logoutCmd, syncCmd, whoamiCmd)
}
