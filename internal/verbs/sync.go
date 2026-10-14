package verbs

import (
	"context"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/sync"
)

// Account verbs. Login takes a password, so it is CLI-only; sync and
// whoami are both doors.

type SyncIn struct {
	Host string `json:"host,omitempty" jsonschema:"API host. Default: GEARS_API, else the saved default, else api.gearsos.com."`
}

// Sync pushes local desk, session, artifact and board rows changed since
// the last push, then pulls and applies changes after the saved cursor.
func Sync(c Ctx, in SyncIn) (*sync.Report, error) {
	host := in.Host
	if host == "" {
		host = api.Host()
	}
	cl, err := api.Open(host)
	if err != nil {
		return nil, err
	}
	return sync.Run(context.Background(), cl, c.Project)
}

type WhoamiIn struct{}

type WhoamiOut struct {
	Host      string `json:"host"`
	LoggedIn  bool   `json:"loggedIn"`
	Label     string `json:"label,omitempty"`
	TenantID  string `json:"tenantId,omitempty"`
	Workspace string `json:"workspace"`
	Project   string `json:"project"`
	Error     string `json:"error,omitempty"`
	// Agent is the label this install acts as locally, where it came from
	// and how to change it (GRS-21). Label above is the device's label on
	// the API.
	Agent AgentInfo `json:"agent"`
}

// Whoami reports the API host, whether a token is saved and still accepted,
// and the workspace this project syncs to.
func Whoami(c Ctx, _ WhoamiIn) (*WhoamiOut, error) {
	out := &WhoamiOut{Host: api.Host(), Workspace: c.Project.WorkspaceID(), Project: c.Project.ID, Agent: c.agentInfo()}
	cl, err := api.Open(out.Host)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	me, err := cl.Me(context.Background())
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.LoggedIn, out.Label, out.TenantID = true, me.Label, me.TenantID
	return out, nil
}
