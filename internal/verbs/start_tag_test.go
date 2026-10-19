package verbs

import (
	"net/http/httptest"
	"testing"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
)

// GRS-105: runbook_start / gears runbook start --tag sends the tag.
func TestRunbookStartSendsTag(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	c := Ctx{Project: fakeapi.Workspace(t), Agent: "claude-local"}
	if _, err := RunbookStart(c, RunbookStartIn{Runbook: "ticket-development", Tag: "sprint-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RunbookStart(c, RunbookStartIn{Runbook: "demo", Steps: []api.StepDef{{ID: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if len(f.Starts) != 2 || f.Starts[0].Tag != "sprint-1" || f.Starts[1].Tag != "" {
		t.Fatalf("starts: %+v", f.Starts)
	}
}

// GRS-102: runbook next asks as the acting agent, so gears-api can tell the
// developer (waits on a reviewer) from the reviewer.
func TestRunbookNextSendsAgentLabel(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	defer srv.Close()
	fakeapi.Login(t, srv.URL)
	c := Ctx{Project: fakeapi.Workspace(t), Agent: "claude-local"}
	out, err := RunbookStart(c, RunbookStartIn{Runbook: "demo", Steps: []api.StepDef{{ID: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunbookNext(c, RunbookNextIn{Run: out.Run.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := RunbookNext(Ctx{Project: c.Project, Agent: "grok-web"}, RunbookNextIn{Run: out.Run.ID}); err != nil {
		t.Fatal(err)
	}
	if len(f.NextLabels) != 2 || f.NextLabels[0] != "claude-local" || f.NextLabels[1] != "grok-web" {
		t.Fatalf("labels: %q", f.NextLabels)
	}
}
