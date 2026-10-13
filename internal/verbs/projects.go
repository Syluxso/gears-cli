package verbs

import (
	"context"
	"fmt"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
)

// Projects verbs: the workspace root's manifest (.gears/projects.yaml) of
// the git repos under projects/. A cache of the API's project list; sync
// makes every non-missing entry exist there.

type ProjectsListIn struct{}

type ProjectsListOut struct {
	Root      string                  `json:"root"`
	Workspace string                  `json:"workspace"`
	Current   string                  `json:"current,omitempty"`
	Projects  []project.ManifestEntry `json:"projects"`
}

// ProjectsList prints the manifest as it stands; it does not scan.
func ProjectsList(c Ctx, _ ProjectsListIn) (*ProjectsListOut, error) {
	out := &ProjectsListOut{Root: c.Project.Root, Workspace: c.Project.WorkspaceID(), Projects: []project.ManifestEntry{}}
	if c.Current != nil {
		out.Current = c.Current.ID
	}
	if !c.Project.IsWorkspace() {
		// A standalone project binding is its own one-entry list.
		out.Projects = append(out.Projects, project.ManifestEntry{ID: c.Project.ID, Slug: c.Project.Slug, Path: "."})
		return out, nil
	}
	m, err := project.LoadManifest(c.Project)
	if err != nil {
		return nil, err
	}
	out.Projects = append(out.Projects, m.Projects...)
	return out, nil
}

type ProjectsScanIn struct {
	Offline bool `json:"offline,omitempty" jsonschema:"Do not ask gears-api which project already has each remote; new repos get a fresh id and sync reconciles it later."`
}

type ProjectsScanOut struct {
	Root     string                  `json:"root"`
	Online   bool                    `json:"online"`
	Added    []string                `json:"added"`
	Missing  []string                `json:"missing"`
	Returned []string                `json:"returned"`
	Adopted  []string                `json:"adopted"`
	Updated  []string                `json:"updated"`
	Notes    []string                `json:"notes"`
	Projects []project.ManifestEntry `json:"projects"`
}

// ProjectsScan looks for git repos under <root>/projects/ and records them.
// When logged in, a repo whose remote the API already holds in this
// workspace takes that project's id; offline, a new repo gets a UUID v7 and
// sync adopts the API's id later if it turns out to know the remote.
func ProjectsScan(c Ctx, in ProjectsScanIn) (*ProjectsScanOut, error) {
	out := &ProjectsScanOut{Root: c.Project.Root, Added: []string{}, Missing: []string{}, Returned: []string{},
		Adopted: []string{}, Updated: []string{}, Notes: []string{}}
	var lookup project.RemoteLookup
	if !in.Offline {
		lookup = remoteLookup(c.Project.WorkspaceID(), out)
	}
	res, err := project.Scan(c.Project, lookup)
	if err != nil {
		return nil, err
	}
	out.Added, out.Missing, out.Returned, out.Adopted, out.Updated = nz(res.Added), nz(res.Missing), nz(res.Returned), nz(res.Adopted), nz(res.Updated)
	out.Projects = append([]project.ManifestEntry{}, res.Manifest.Projects...)
	return out, nil
}

// remoteLookup asks gears-api which project in workspace ws has a remote.
// No token, or the API unreachable, means offline: say so once and stop
// asking. A remote the account holds in another workspace is not adopted.
func remoteLookup(ws string, out *ProjectsScanOut) project.RemoteLookup {
	cl, err := api.Open(api.Host())
	if err != nil {
		out.Notes = append(out.Notes, "offline: "+err.Error()+"; new repos get fresh ids, sync reconciles them")
		return nil
	}
	out.Online = true
	return func(remote string) (string, bool) {
		if !out.Online {
			return "", false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b, err := cl.Bind(ctx, remote)
		if err != nil {
			out.Online = false
			out.Notes = append(out.Notes, "offline: "+err.Error()+"; new repos get fresh ids, sync reconciles them")
			return "", false
		}
		if b == nil {
			return "", false
		}
		if b.Workspace.ID != ws {
			out.Notes = append(out.Notes, fmt.Sprintf("%s is project %s in another workspace (%s); not adopted", remote, b.Project.ID, b.Workspace.Slug))
			return "", false
		}
		return b.Project.ID, true
	}
}
