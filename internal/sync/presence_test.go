package sync

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/fakeapi"
)

// GRS-37: the board stream is presence. Live says who holds it (the agent
// label over the device's login label) and gears run's auto-sync interval;
// the API then lists that connection until it drops.
func TestLiveSaysWhoHoldsTheStream(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := liveCache(t)
	lr := startLive(t, srv.URL, p, func(o *LiveOptions) {
		o.Client.Label = "login-label"
		o.Hello = api.StreamHello{Label: "claude-local", SyncMinutes: 5}
	})
	waitFor(t, 2*time.Second, "subscribed", func() bool { return f.Subscribers() == 1 })
	h := f.Hello()
	if h.Get("X-Gears-Label") != "claude-local" || h.Get("X-Gears-Sync-Interval") != "5" {
		t.Fatalf("stream headers: label %q interval %q", h.Get("X-Gears-Label"), h.Get("X-Gears-Sync-Interval"))
	}
	pr, err := api.Anonymous(srv.URL).Presence(context.Background(), workspaceOf(p))
	if err != nil || pr.Count != 1 || pr.Connections[0].Label != "claude-local" || pr.Connections[0].SyncIntervalMinutes != 5 {
		t.Fatalf("presence: %+v %v", pr, err)
	}
	lr.cancel()
	waitFor(t, 2*time.Second, "unsubscribed", func() bool { return f.Subscribers() == 0 })
	if pr, _ := api.Anonymous(srv.URL).Presence(context.Background(), workspaceOf(p)); pr.Count != 0 || pr.Connections == nil {
		t.Fatalf("after disconnect: %+v", pr)
	}
}

// No hello (gears watch, an old caller): no interval header, and the label
// is the device's login label as before.
func TestLiveWithoutHello(t *testing.T) {
	f := fakeapi.New("WS")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := liveCache(t)
	startLive(t, srv.URL, p, func(o *LiveOptions) { o.Client.Label = "login-label" })
	waitFor(t, 2*time.Second, "subscribed", func() bool { return f.Subscribers() == 1 })
	h := f.Hello()
	if h.Get("X-Gears-Label") != "login-label" || h.Get("X-Gears-Sync-Interval") != "" {
		t.Fatalf("stream headers: label %q interval %q", h.Get("X-Gears-Label"), h.Get("X-Gears-Sync-Interval"))
	}
}
