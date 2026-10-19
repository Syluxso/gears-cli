package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-92: the --hook command around verbs.StopHook: stdin is optional and
// never waited on for long, the answer is one JSON line or nothing, and the
// label comes from GEARS_AGENT like every other command.
func hookRun(t *testing.T, stdin io.Reader) (string, time.Duration) {
	t.Helper()
	var out bytes.Buffer
	oldIn, oldOut := hookStdin, hookStdout
	hookStdin, hookStdout = stdin, &out
	defer func() { hookStdin, hookStdout = oldIn, oldOut }()
	start := time.Now()
	runStopHook()
	return out.String(), time.Since(start)
}

func TestStopHookCommand(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)
	t.Setenv("GEARS_AGENT", "claude-local")
	t.Setenv("GEARS_HOOK_DEBUG", "")
	t.Chdir(t.TempDir()) // never the real workspace above this repo
	payload := func(active bool) io.Reader {
		b, _ := json.Marshal(map[string]any{"session_id": "sess-1", "cwd": p.Root, "hook_event_name": "Stop", "stop_hook_active": active,
			"last_assistant_message": "done"})
		return bytes.NewReader(b)
	}

	// No run: nothing.
	if out, _ := hookRun(t, payload(false)); out != "" {
		t.Fatalf("no run: %q", out)
	}
	c := verbs.Ctx{Project: p, Agent: "claude-local"}
	if _, err := verbs.RunbookStart(c, verbs.RunbookStartIn{Runbook: "demo", Steps: []api.StepDef{{ID: "work"}}}); err != nil {
		t.Fatal(err)
	}
	out, _ := hookRun(t, payload(false))
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["decision"] != "block" || !strings.Contains(got["reason"], "Gears run demo") ||
		strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("block: %q %v", out, err)
	}
	// Continuing without progress: nothing.
	if out, _ := hookRun(t, payload(true)); out != "" {
		t.Fatalf("loop guard: %q", out)
	}
	// Another label: nothing.
	t.Setenv("GEARS_AGENT", "grok-cli")
	if out, _ := hookRun(t, payload(false)); out != "" {
		t.Fatalf("other label: %q", out)
	}
	t.Setenv("GEARS_AGENT", "claude-local")

	// Garbage or empty stdin is no input: the cwd is the process's, which
	// is not a workspace here, so nothing.
	if out, _ := hookRun(t, strings.NewReader("not json")); out != "" {
		t.Fatalf("garbage: %q", out)
	}
	if out, _ := hookRun(t, strings.NewReader("")); out != "" {
		t.Fatalf("empty: %q", out)
	}
	// A stdin that never closes is not waited on past its budget.
	pr, pw := io.Pipe()
	defer pw.Close()
	if out, took := hookRun(t, pr); out != "" || took > 2*time.Second {
		t.Fatalf("open stdin: %q in %s", out, took)
	}
	// A slow gears-api: nothing, within about the budget.
	f.RunsDelay = 5 * time.Second
	if out, took := hookRun(t, payload(false)); out != "" || took > 3500*time.Millisecond {
		t.Fatalf("slow: %q in %s", out, took)
	}
}
