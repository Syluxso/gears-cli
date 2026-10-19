package verbs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/runs"
)

// The Claude Code Stop hook (GRS-92): gears runbook next --hook. When a
// running run this agent label OWNS waits on the agent, the hook answers
// {"decision":"block","reason":...} so the agent keeps going with the
// step's instruction; in every other case it answers nothing and the agent
// stops. It fails open: no run, a finished run, a step waiting on a person
// or the system, a deleted workspace, not logged in, gears-api offline, an
// error or a slow answer all let the stop happen. It never writes the
// local run cache; gears-api is the only source it blocks on.

// StopHookIn is what Claude Code sends a Stop hook on stdin.
type StopHookIn struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// StopHookOut is the block answer; nil means let the agent stop.
type StopHookOut struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// StopHookState is the last block per Claude Code session, kept in
// .gears/.gearbox/stop-hook.json: when Claude is already continuing because
// of this hook (stop_hook_active) and the run has not moved since, the
// agent could not make progress, so the stop is allowed.
type StopHookState struct {
	RunID   string    `json:"runId"`
	StepID  string    `json:"stepId"`
	Version int       `json:"version"`
	At      time.Time `json:"at"`
}

const stopHookStateFile = "stop-hook.json"

// StopHook decides. why says what it decided and why (for the debug log).
func StopHook(c Ctx, in StopHookIn, timeout time.Duration) (out *StopHookOut, why string) {
	defer func() {
		if r := recover(); r != nil {
			out, why = nil, fmt.Sprintf("allow: panic %v", r)
		}
	}()
	if c.Project == nil {
		return nil, "allow: not in a Gears workspace"
	}
	if err := c.CheckLive(); err != nil {
		return nil, "allow: " + err.Error()
	}
	ws := c.Project.WorkspaceID()
	if ws == "" {
		return nil, "allow: workspace not on gears-api"
	}
	cl, err := runsClient()
	if err != nil {
		return nil, "allow: " + err.Error()
	}
	agent := c.agent()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	list, _, err := cl.ListRunsPage(ctx, ws, api.RunsFilter{Status: "running", Owner: agent, Limit: 20})
	if err != nil {
		return nil, "allow: " + err.Error()
	}
	var mine []api.Run
	for _, r := range list {
		if r.Status == "running" && runOwner(r) == agent {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return nil, "allow: " + agent + " owns no running run"
	}
	// The most recently updated first; a step the record already shows as
	// the system's or a person's is passed over without asking.
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].UpdatedAt.After(mine[j].UpdatedAt) })
	var next *api.RunNext
	var run api.Run
	for _, r := range mine {
		// A card.wait is the system's, but while audit tickets are open the
		// agent works them (GRS-100): ask gears-api, which says so.
		if cur := runs.Current(&r); cur != nil && cur.Kind != "card.wait" && (cur.Executor == "system" || cur.Kind == "approval" || cur.Kind == "gate") {
			continue
		}
		n, err := cl.NextRun(ctx, r.ID, agent)
		if err != nil {
			return nil, "allow: " + err.Error()
		}
		if n.WaitingOn == "agent" && !n.Finished && n.Step != nil {
			next, run = n, r
			break
		}
	}
	if next == nil {
		return nil, "allow: no run owned by " + agent + " waits on the agent"
	}
	version := next.Version
	if version == 0 {
		version = run.Version
	}
	cur := StopHookState{RunID: next.RunID, StepID: next.Step.ID, Version: version, At: time.Now().UTC()}
	states := readHookStates(c)
	if prev, ok := states[in.SessionID]; ok && in.StopHookActive && in.SessionID != "" &&
		prev.RunID == cur.RunID && prev.StepID == cur.StepID && prev.Version == cur.Version {
		return nil, "allow: already continuing for this step and the run has not moved"
	}
	if in.SessionID != "" {
		states[in.SessionID] = cur
		writeHookStates(c, states)
	}
	title := next.Step.Title
	if title == "" {
		title = next.Step.ID
	}
	short := next.RunID
	if len(short) > 12 {
		short = short[:12]
	}
	instr := strings.TrimSpace(next.Instructions)
	instr = strings.TrimSuffix(instr, ".")
	reason := fmt.Sprintf("Gears run %s (%s) is waiting on you: step %s — %s. Do it, then call runbook_step_complete with evidence; or runbook_step_skip with a reason; stop by telling the user why.",
		next.Runbook, short, title, instr)
	return &StopHookOut{Decision: "block", Reason: reason}, "block: " + next.RunID + " step " + next.Step.ID
}

func hookStatePath(c Ctx) string {
	return filepath.Join(c.Project.GearboxPath(), stopHookStateFile)
}

func readHookStates(c Ctx) map[string]StopHookState {
	out := map[string]StopHookState{}
	raw, err := os.ReadFile(hookStatePath(c))
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]StopHookState{}
	}
	return out
}

// writeHookStates keeps a day of sessions.
func writeHookStates(c Ctx, states map[string]StopHookState) {
	for k, v := range states {
		if time.Since(v.At) > 24*time.Hour {
			delete(states, k)
		}
	}
	b, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return
	}
	p := hookStatePath(c)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		if err := os.Rename(tmp, p); err != nil {
			_ = os.Remove(tmp)
		}
	}
}
