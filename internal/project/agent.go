package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentFile holds the workspace's agent name on this machine (GRS-63): the
// label commands run as when neither -a nor $GEARS_AGENT says otherwise.
// It lives in .gearbox/, so it is machine state and never committed.
const AgentFile = "agent"

// AgentNameMax is the longest agent name accepted.
const AgentNameMax = 64

// AgentPath is where the workspace's agent name is saved.
func (p *Project) AgentPath() string { return AgentPathAt(p.Root) }

// AgentName is the saved agent name, or "".
func (p *Project) AgentName() string { return ReadAgentName(p.Root) }

// AgentPathAt is the agent name file for the workspace rooted at root.
func AgentPathAt(root string) string {
	return filepath.Join(root, Dir, GearboxDir, AgentFile)
}

// ReadAgentName returns the agent name saved for the workspace at root, or
// "" when none is saved or the saved one is not a valid name.
func ReadAgentName(root string) string {
	data, err := os.ReadFile(AgentPathAt(root))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(data))
	if ValidateAgentName(name) != nil {
		return ""
	}
	return name
}

// WriteAgentName saves name as the agent name for the workspace at root.
func WriteAgentName(root, name string) error {
	if err := ValidateAgentName(name); err != nil {
		return err
	}
	path := AgentPathAt(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(name+"\n"), 0o644)
}

// ValidateAgentName accepts 1 to 64 of a-z 0-9 - _ . + (e.g. claude-local,
// claude-code+local). The name is the label on desk items, session entries
// and card activity, so it stays plain enough to type and to match.
func ValidateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name is empty")
	}
	if len(name) > AgentNameMax {
		return fmt.Errorf("agent name %q: at most %d characters", name, AgentNameMax)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '+':
		default:
			return fmt.Errorf("agent name %q: lowercase letters, digits and - _ . + only", name)
		}
	}
	return nil
}

// CheckInitDir refuses a directory that sits under an existing binding, the
// same check Init makes, so a caller can refuse before asking anything.
func CheckInitDir(dir string) error {
	if parent := filepath.Dir(dir); parent != dir {
		if anc, err := Find(parent); err == nil {
			return &UnderBindingError{Dir: dir, Root: anc.Root, Workspace: anc.IsWorkspace()}
		}
	}
	return nil
}
