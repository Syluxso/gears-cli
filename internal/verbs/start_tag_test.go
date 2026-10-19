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
