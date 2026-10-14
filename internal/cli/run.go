package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/mcpserver"
	"github.com/Syluxso/gears-cli/internal/sync"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

var (
	runDir          string
	runSyncInterval int
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Serve the local MCP over stdio",
	Long: `The agent's door. Serves every Gears verb as an MCP tool over stdin and
stdout for the project in the current directory (or --dir).

Cards sync immediately (GRS-51): while it runs, the server holds the
workspace's live board open (when logged in) and applies every change made
elsewhere (the app, another machine) within a moment. Card writes push at
once. Desk, sessions and artifacts still move with sync. GEARS_CARD_SYNC=off
turns the card behaviour off. Logs go to stderr; stdout is the MCP channel.

--sync-interval N (minutes; 0, the default, is off) runs a full sync
(desk, sessions, artifacts, cards) once at startup and then every N minutes
(GRS-25). GEARS_SYNC_INTERVAL sets it from the harness env; the flag wins.
It shares the sync lock with the live board and card writes, so syncs never
overlap. Not logged in or offline: one note on stderr, then it skips
quietly until that changes.

Harness config (Claude Code, .mcp.json):
  { "mcpServers": { "gears": { "command": "gears", "args": ["run", "--dir", "/path/to/repo"] } } }

Set GEARS_AGENT in the harness env so tools act as that agent.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := runDir
		if dir == "" {
			dir = mustCwd()
		}
		c, err := ctxAt(dir)
		if err != nil {
			return err
		}
		c.Notes = nil // notes ride in the tool results; stdout is the MCP channel
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fmt.Fprintf(os.Stderr, "gears mcp: project %s, agent %s, %s\n", c.Project.Slug, c.Agent, c.Project.Root)
		go liveBoard(ctx, c, nil)
		every, err := syncInterval(cmd.Flags().Changed("sync-interval"), runSyncInterval, os.Getenv("GEARS_SYNC_INTERVAL"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "gears sync: %s; periodic sync off\n", err)
		}
		if every > 0 {
			fmt.Fprintf(os.Stderr, "gears sync: every %d min\n", int(every/time.Minute))
			go periodicSync(ctx, c, every)
		}
		s := mcpserver.New(c, Version)
		return mcpserver.ServeStdio(ctx, s)
	},
}

// liveBoard holds the workspace's board stream open until ctx ends (GRS-51).
// It never fails its caller: no login, no binding, an API without the live
// board are each one line on stderr, and cards still sync on every write
// and read. onChange receives applied changes (gears watch prints them).
func liveBoard(ctx context.Context, c verbs.Ctx, onChange func(sync.CardChange)) error {
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "gears live: "+format+"\n", args...)
	}
	if sync.Disabled() {
		logf("card sync is off (GEARS_CARD_SYNC)")
		return errLiveOff
	}
	host := api.Host()
	cl, err := api.Open(host)
	if err != nil {
		logf("%s; the board updates on gears sync", err)
		return err
	}
	if !sync.CardsReady(c.Project) {
		logf("cards have not synced here yet; run gears sync once, then the board stays live")
		return sync.ErrCardsNotReady
	}
	return sync.Live(ctx, sync.LiveOptions{Client: cl, Project: c.Project, Logf: logf, OnChange: onChange})
}

var errLiveOff = errors.New("card sync is off (GEARS_CARD_SYNC)")

// syncInterval is gears run's periodic sync interval (GRS-25): the flag
// when given, else GEARS_SYNC_INTERVAL, in whole minutes; 0 is off.
func syncInterval(flagSet bool, flagMinutes int, env string) (time.Duration, error) {
	n := flagMinutes
	if !flagSet {
		env = strings.TrimSpace(env)
		if env == "" {
			return 0, nil
		}
		v, err := strconv.Atoi(env)
		if err != nil {
			return 0, fmt.Errorf("GEARS_SYNC_INTERVAL=%q is not a whole number of minutes", env)
		}
		n = v
	}
	if n < 0 {
		return 0, fmt.Errorf("sync interval %d: minutes cannot be negative", n)
	}
	return time.Duration(n) * time.Minute, nil
}

// periodicSync runs verbs.Sync at once and then every interval until ctx
// ends, one line on stderr per sync (GRS-25).
func periodicSync(ctx context.Context, c verbs.Ctx, every time.Duration) {
	autoSync{
		ticker: func() (<-chan time.Time, func()) {
			t := time.NewTicker(every)
			return t.C, t.Stop
		},
		run: func(ctx context.Context) (*sync.Report, error) { return verbs.SyncCtx(ctx, c, verbs.SyncIn{}) },
		log: os.Stderr,
	}.loop(ctx)
}

// autoSync is the periodic sync's loop with its clock, its sync and its
// log swappable in tests.
type autoSync struct {
	ticker func() (<-chan time.Time, func())
	run    func(context.Context) (*sync.Report, error)
	log    io.Writer
}

func (a autoSync) loop(ctx context.Context) {
	tick, stop := a.ticker()
	defer stop()
	quiet := "" // the skip reason last logged; "" after a sync went through
	once := func() {
		r, err := a.run(ctx)
		if ctx.Err() != nil {
			return // shutting down; nothing to say
		}
		if err != nil {
			why := skipReason(err)
			if why == "" {
				quiet = ""
				fmt.Fprintf(a.log, "gears sync: %s\n", err)
				return
			}
			if why != quiet {
				quiet = why
				fmt.Fprintf(a.log, "gears sync: skipped, %s; skipping quietly until that changes\n", why)
			}
			return
		}
		quiet = ""
		fmt.Fprintf(a.log, "gears sync: pushed %d, pulled %d\n", r.Pushed, r.Pulled)
	}
	once()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			once()
		}
	}
}

// skipReason is the quiet reason a periodic sync could not run (not
// logged in, offline), or "" for an error worth a line every time.
func skipReason(err error) string {
	switch {
	case errors.Is(err, api.ErrNotLoggedIn):
		return "not logged in (gears login)"
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(err.Error(), "unreachable"):
		return "offline (" + api.Host() + " unreachable)"
	}
	return ""
}

func init() {
	runCmd.Flags().StringVar(&runDir, "dir", "", "project directory (default: current directory)")
	runCmd.Flags().IntVar(&runSyncInterval, "sync-interval", 0, "sync every N minutes, once at startup too; 0 is off (env GEARS_SYNC_INTERVAL)")
	root.AddCommand(runCmd)
}
