package verbs

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
)

// GRS-37: status carries who holds the board stream right now, from
// gears-api; nothing when the API cannot say.
func TestStatusPresence(t *testing.T) {
	f := fakeapi.New("WS")
	since := time.Date(2026, 10, 15, 7, 12, 0, 0, time.UTC)
	f.Others = []api.PresenceConn{{UserID: "u-darryn", Name: "Darryn S", Email: "d@example.test", AuthMethod: "iam", ConnectedAt: since}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fakeapi.Login(t, srv.URL)
	p := fakeapi.Workspace(t)

	cl, err := api.Open(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	body, err := cl.BoardEventsAs(ctx, p.WorkspaceID(), api.StreamHello{Label: "claude-local", SyncMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); body.Close() })

	out, err := Status(Ctx{Project: p, Agent: "claude-local"}, StatusIn{})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.Presence
	if pr == nil || pr.Count != 2 || len(pr.Connections) != 2 {
		t.Fatalf("presence: %+v", pr)
	}
	if c := pr.Connections[0]; c.Name != "Darryn S" || c.AuthMethod != "iam" {
		t.Fatalf("browser: %+v", c)
	}
	if c := pr.Connections[1]; c.Label != "claude-local" || c.SyncIntervalMinutes != 5 || c.AuthMethod != "device" {
		t.Fatalf("gears run: %+v", c)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"presence":{"count":2`) {
		t.Fatalf("json: %s", b)
	}

	// An API from before presence: no presence, and status still answers.
	f.PresenceStatus = 404
	out, err = Status(Ctx{Project: p, Agent: "claude-local"}, StatusIn{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Presence != nil {
		t.Fatalf("presence from a 404: %+v", out.Presence)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), `"presence"`) {
		t.Fatalf("json carries presence: %s", b)
	}
}

// Not logged in: no API call, no presence.
func TestStatusPresenceNotLoggedIn(t *testing.T) {
	if p := apiPresence("0192aaaa-0000-7000-8000-000000000000"); p != nil {
		t.Fatalf("presence while not logged in: %+v", p)
	}
}
