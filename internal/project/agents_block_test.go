package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GRS-89: the AGENTS.md block carries the runbook agent loop; an older
// block is brought up to date in place, the rest of the file kept.
func TestAgentsBlockAgentLoop(t *testing.T) {
	dir := t.TempDir()
	var res InitResult
	if err := ensureAgentsBlock(dir, &res); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	for _, want := range []string{"runbook_next", "runbook_step_complete", "runbook_runs {mine: true", "waitingOn human", "Two human gates"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("block lacks %q:\n%s", want, b)
		}
	}
	old := "# Mine\n\n" + agentsStart + "\n## Gears\n\nold text\n" + agentsEnd + "\n\n## After\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	res = InitResult{}
	if err := ensureAgentsBlock(dir, &res); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	s := string(b)
	if !strings.HasPrefix(s, "# Mine") || !strings.Contains(s, "## After") || strings.Contains(s, "old text") || !strings.Contains(s, "runbook_next") ||
		strings.Count(s, agentsStart) != 1 || !strings.Contains(res.Actions[0], "updated") {
		t.Fatalf("updated block:\n%s %v", s, res.Actions)
	}
	res = InitResult{}
	_ = ensureAgentsBlock(dir, &res)
	if !strings.Contains(res.Actions[0], "kept") {
		t.Fatalf("current block: %v", res.Actions)
	}
}
