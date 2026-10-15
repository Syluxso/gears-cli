package verbs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/session"
)

// GRS-21: status and whoami carry {agent: {label, source, howToChange}}.
func TestAgentInfoInStatusAndWhoami(t *testing.T) {
	_, p, _ := workspace(t)
	stubAPI(t, StatusAPI{Host: "http://api.test", State: APIOffline}, nil)
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	t.Setenv("GEARS_AGENT", "")
	defer func(f func() string) { session.LoginLabel = f }(session.LoginLabel)
	session.LoginLabel = nil

	for _, c := range []struct {
		ctx                Ctx
		label, source, say string
	}{
		{Ctx{Project: p, Agent: "grok", AgentSource: session.AgentFromFlag}, "grok", "flag", "-a/--agent"},
		{Ctx{Project: p, Agent: "claude-local", AgentSource: session.AgentFromEnv}, "claude-local", "env", "GEARS_AGENT"},
		{Ctx{Project: p, Agent: "ws-name", AgentSource: session.AgentFromWorkspace}, "ws-name", "workspace", "gears init --name"},
		{Ctx{Project: p, Agent: "codex", AgentSource: session.AgentFromLogin}, "codex", "login", "gears login --label"},
		{Ctx{Project: p}, "local", "default", `"local"`},
	} {
		st, err := Status(c.ctx, StatusIn{})
		if err != nil {
			t.Fatal(err)
		}
		who, err := Whoami(c.ctx, WhoamiIn{})
		if err != nil {
			t.Fatal(err)
		}
		for name, v := range map[string]any{"status": st, "whoami": who} {
			raw, _ := json.Marshal(v)
			var m struct {
				Agent struct {
					Label       string   `json:"label"`
					Source      string   `json:"source"`
					SourceText  string   `json:"sourceText"`
					HowToChange []string `json:"howToChange"`
				} `json:"agent"`
			}
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			a := m.Agent
			if a.Label != c.label || a.Source != c.source || !strings.Contains(a.SourceText, c.say) {
				t.Fatalf("%s %s: %+v", name, c.label, a)
			}
			all := strings.Join(a.HowToChange, "\n")
			for _, want := range []string{"gears -a <name>", "export GEARS_AGENT=<name>", "set GEARS_AGENT=<name>", `$env:GEARS_AGENT`, ".mcp.json", `"GEARS_AGENT"`, "gears init --name <name>", "gears login --label <name>"} {
				if !strings.Contains(all, want) {
					t.Fatalf("%s howToChange lacks %q: %v", name, want, a.HowToChange)
				}
			}
		}
	}
}
