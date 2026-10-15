package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	initSlug    string
	initPrefix  string
	initOffline bool
	initName    string
)

// stdinIsTerminal reports whether init may ask for the agent name. Tests
// swap it (and stdin) to drive the prompt.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a Gears workspace in this directory",
	Long: `Create a Gears workspace in the current directory.

First, the agent's name (GRS-63): the string this agent is known by here.
Desk items are addressed to it and sent from it, and session log entries
and card activity carry it. It comes from --name, else -a/--agent, else
$GEARS_AGENT; a name saved by an earlier init is kept without asking
(only --name replaces it); otherwise init asks, suggesting the gears login
label if there is one. With no terminal to ask on and no name given, init
stops before creating anything: pass --name <label> or set GEARS_AGENT.
Names are 1 to 64 of a-z 0-9 - _ . + (e.g. claude-local).

The name is saved for this workspace on this machine in
.gears/.gearbox/agent (never committed) and is the label every command
here runs as when neither -a nor GEARS_AGENT is set (before the gears
login label). gears status and gears whoami show it with source
"workspace".

Then it writes .gears/project.yaml, creates .gears/artifacts/, adds .gears/ to
.gitignore, and writes a short Gears block into AGENTS.md. No skeleton
tree. At a workspace root it also writes a stub architecture doc,
.gears/artifacts/arch--primary.md (the master design doc gears status
always shows in full). Safe to run again at the root: existing files,
the architecture doc included, are left alone.

At a workspace root (a directory with projects/), init also scans
projects/*/ for git repos and records them in .gears/projects.yaml, the same
as gears projects scan.

Workspace rules:
  - One .gears per workspace, at its root. Run init in a directory with no
    .gears above it, and that directory becomes the workspace root.
  - init anywhere inside an existing workspace (any .gears above) is
    refused and names the root. No .gears is ever created below it.
  - Every other command run anywhere under the workspace walks up to the
    root's .gears and works there; outside any workspace it says so.
  - A nested .gears left over from an older version or another machine is
    ignored: the outermost one wins, and commands print one warning naming
    the stray so you can delete it.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		// Refuse a nested init before asking for anything.
		if err := project.CheckInitDir(dir); err != nil {
			return err
		}
		name, from, save, err := initAgentName(dir, os.Stderr)
		if err != nil {
			return err
		}
		res, err := project.Init(dir, initSlug, initPrefix)
		if err != nil {
			return err
		}
		for _, line := range res.Actions {
			fmt.Println(" ", line)
		}
		p := res.Project
		agentRel := filepath.ToSlash(mustRel(dir, p.AgentPath()))
		if save {
			if err := project.WriteAgentName(p.Root, name); err != nil {
				return err
			}
			fmt.Printf("  wrote   %s (agent name %s, from %s)\n", agentRel, name, from)
		} else {
			fmt.Printf("  kept    %s (agent name %s)\n", agentRel, name)
		}
		if p.IsWorkspace() {
			// The workspace's architecture doc (GRS-41): a stub once, never
			// overwritten on a re-run.
			a, created, err := artifact.InitArch(p, name)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, a.Path)
			if created {
				fmt.Println("  wrote  ", filepath.ToSlash(rel), "(architecture stub: fill it in; gears status always shows it)")
			} else {
				fmt.Println("  kept   ", filepath.ToSlash(rel))
			}
		}
		if p.IsWorkspace() && (dirExists(filepath.Join(dir, project.ProjectsDir)) || p.HasManifest()) {
			out, err := verbs.ProjectsScan(verbs.Ctx{Project: p, Agent: name}, verbs.ProjectsScanIn{Offline: initOffline})
			if err != nil {
				return err
			}
			fmt.Printf("  scanned %s/: %d project%s in .gears/%s\n", project.ProjectsDir, len(out.Projects), plural(len(out.Projects), "", "s"), project.ManifestFile)
			printScan(out)
		}
		fmt.Printf("\nBound %s to %q.\nAgent name: %s (saved in %s; used here when neither -a nor GEARS_AGENT is set).\nNext: gears status\n", dir, p.Slug, name, agentRel)
		return nil
	},
}

// initAgentName decides the agent name init saves (GRS-63) and whether to
// write it: --name always (it replaces a saved name); else a saved name is
// kept as it is; else -a, then $GEARS_AGENT; else a prompt on a terminal,
// suggesting the login label. With none of those it refuses, before
// anything is created. from says where the name came from.
func initAgentName(dir string, w io.Writer) (name, from string, save bool, err error) {
	if n := strings.TrimSpace(initName); n != "" {
		if err := project.ValidateAgentName(n); err != nil {
			return "", "", false, fmt.Errorf("--name: %w", err)
		}
		return n, "--name", project.ReadAgentName(dir) != n, nil
	}
	if n := project.ReadAgentName(dir); n != "" {
		return n, "saved", false, nil
	}
	for _, c := range []struct{ v, from string }{
		{globalAgent, "-a/--agent"},
		{os.Getenv("GEARS_AGENT"), "GEARS_AGENT"},
	} {
		if n := strings.TrimSpace(c.v); n != "" {
			if err := project.ValidateAgentName(n); err != nil {
				return "", "", false, fmt.Errorf("%s: %w", c.from, err)
			}
			return n, c.from, true, nil
		}
	}
	if !stdinIsTerminal() {
		return "", "", false, errors.New("gears init needs the agent's name: the label desk items, session entries and card activity carry in this workspace. " +
			"No terminal to ask on, so pass it: gears init --name <label> (or set GEARS_AGENT=<label>). Nothing was created")
	}
	suggest := ""
	if session.LoginLabel != nil {
		if l := strings.TrimSpace(session.LoginLabel()); project.ValidateAgentName(l) == nil {
			suggest = l
		}
	}
	fmt.Fprintln(w, "Name this agent. The name is the string it is known by in this workspace:")
	fmt.Fprintln(w, "desk items are addressed to it and sent from it, and session log entries and")
	fmt.Fprintln(w, "card activity carry it. Lowercase letters, digits and - _ . + (e.g. claude-local).")
	label := "Agent name: "
	if suggest != "" {
		label = fmt.Sprintf("Agent name [%s]: ", suggest)
	}
	for {
		fmt.Fprint(w, label)
		line, rerr := stdin.ReadString('\n')
		n := strings.TrimSpace(line)
		if n == "" {
			switch {
			case suggest != "":
				n = suggest // empty Enter takes the suggestion
			case rerr != nil:
				return "", "", false, errors.New("no agent name given; nothing was created. Run gears init --name <label>")
			default:
				continue
			}
		}
		if verr := project.ValidateAgentName(n); verr != nil {
			fmt.Fprintln(w, " ", verr)
			if rerr != nil {
				return "", "", false, verr
			}
			continue
		}
		return n, "prompt", true, nil
	}
}

func mustRel(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil {
		return r
	}
	return path
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func init() {
	initCmd.Flags().StringVar(&initSlug, "slug", "", "slug (default: directory name)")
	initCmd.Flags().StringVar(&initPrefix, "prefix", "", "card key prefix, e.g. GRS (default: from the slug)")
	initCmd.Flags().StringVar(&initName, "name", "", "the agent's name in this workspace: the label desk items, session entries and card activity carry (asked for when not given)")
	initCmd.Flags().BoolVar(&initOffline, "offline", false, "scan without asking gears-api for known remotes")
	root.AddCommand(initCmd)
}
