package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

// gears runbook next --hook (GRS-92): the Claude Code Stop hook. It reads
// the hook's JSON from stdin (optional: a terminal or an empty stdin is no
// input), decides with verbs.StopHook, and prints {"decision":"block",
// "reason":...} or nothing. It always exits 0 and never writes to stderr,
// unless GEARS_HOOK_DEBUG is set. The whole answer takes at most about
// hookBudget.

var (
	hookBudget              = 2 * time.Second
	hookStdinWait           = 500 * time.Millisecond
	hookStdin     io.Reader = os.Stdin
	hookStdout    io.Writer = os.Stdout
)

func hookDebugf(format string, args ...any) {
	if os.Getenv("GEARS_HOOK_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "gears hook: "+format+"\n", args...)
	}
}

// readHookInput reads the hook JSON, waiting at most hookStdinWait; a
// terminal is not read at all.
func readHookInput() verbs.StopHookIn {
	var in verbs.StopHookIn
	if f, ok := hookStdin.(*os.File); ok {
		if fi, err := f.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
			return in
		}
	}
	got := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(hookStdin, 1<<20))
		got <- b
	}()
	select {
	case b := <-got:
		if s := strings.TrimSpace(string(b)); s != "" {
			if err := json.Unmarshal([]byte(s), &in); err != nil {
				hookDebugf("stdin is not hook JSON: %v", err)
			}
		}
	case <-time.After(hookStdinWait):
		hookDebugf("no stdin within %s", hookStdinWait)
	}
	return in
}

// runStopHook answers the Stop hook. It never returns an error.
func runStopHook() {
	start := time.Now()
	in := readHookInput()
	type result struct {
		out *verbs.StopHookOut
		why string
	}
	left := hookBudget - time.Since(start)
	if left < 200*time.Millisecond {
		left = 200 * time.Millisecond
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{why: fmt.Sprintf("allow: panic %v", r)}
			}
		}()
		dir := strings.TrimSpace(in.Cwd)
		if dir == "" {
			dir = mustCwdQuiet()
		} else if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			dir = mustCwdQuiet()
		}
		c, err := ctxAt(dir)
		if err != nil {
			done <- result{why: "allow: " + err.Error()}
			return
		}
		c.Notes = io.Discard
		out, why := verbs.StopHook(c, in, left)
		done <- result{out, why}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(left + 250*time.Millisecond):
		r.why = "allow: timed out after " + time.Since(start).Round(time.Millisecond).String()
	}
	hookDebugf("%s (%s)", r.why, time.Since(start).Round(time.Millisecond))
	if r.out != nil {
		b, err := json.Marshal(r.out)
		if err == nil {
			fmt.Fprintln(hookStdout, string(b))
		}
	}
}

func mustCwdQuiet() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
