package verbs

import "github.com/Syluxso/gears-cli/internal/session"

// AgentInfo is the acting agent's label, where it came from and how to
// change it (GRS-21). status and whoami carry it so an agent running as
// "local" can see that and fix it.
type AgentInfo struct {
	Label string `json:"label"`
	// Source is flag (gears -a), env (GEARS_AGENT, also how an MCP server
	// gets it), login (gears login --label) or default ("local").
	Source string `json:"source"`
	// SourceText says the same for a person.
	SourceText  string   `json:"sourceText"`
	HowToChange []string `json:"howToChange"`
}

// agentInfo resolves Ctx's agent. A Ctx built with an Agent but no source
// (tests, callers that pass a name) counts as the flag.
func (c Ctx) agentInfo() AgentInfo {
	label, source := c.Agent, c.AgentSource
	if label == "" {
		label, source = session.ResolveAgent("")
	} else if source == "" {
		source = session.AgentFromFlag
	}
	return AgentInfo{Label: label, Source: source, SourceText: agentSourceText(source), HowToChange: AgentHowToChange()}
}

func agentSourceText(source string) string {
	switch source {
	case session.AgentFromFlag:
		return "the -a/--agent flag"
	case session.AgentFromEnv:
		return "the GEARS_AGENT environment variable"
	case session.AgentFromLogin:
		return "the device login label (gears login --label)"
	}
	return `the default ("local"): no -a flag, no GEARS_AGENT, no login label`
}

// AgentHowToChange is every way to set the label, strongest first.
func AgentHowToChange() []string {
	return []string{
		"one command: gears -a <name> <command>   (e.g. gears -a claude-local status)",
		"this shell: export GEARS_AGENT=<name> (bash/zsh) | set GEARS_AGENT=<name> (cmd) | $env:GEARS_AGENT=\"<name>\" (PowerShell)",
		`MCP (gears run): add "env": {"GEARS_AGENT": "<name>"} to the gears server in .mcp.json, then restart the MCP server`,
		"this install's default: gears login --label <name>   (used when neither -a nor GEARS_AGENT is set)",
	}
}
