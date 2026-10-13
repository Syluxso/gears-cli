package verbs

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/gitx"
	"github.com/Syluxso/gears-cli/internal/project"
)

// The workspace half of status (GRS-46): every project in the workspace,
// from the manifest, the directories under projects/ and, when logged in,
// gears-api's project list; each with its tech and a git overview. Read
// only: status never writes the manifest, it says when a scan would.

// StatusProject is one project in the workspace report.
type StatusProject struct {
	ID   string `json:"id,omitempty"`
	Slug string `json:"slug"`
	// Name is the API's name when it has one, else the slug.
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	// Remote is origin as git reports it, else the manifest's, else the API's.
	Remote  string `json:"remote,omitempty"`
	Current bool   `json:"current,omitempty"`
	Listed  bool   `json:"listed"` // in .gears/projects.yaml
	OnDisk  bool   `json:"onDisk"`
	// OnAPI is whether gears-api holds the project; absent when the API was
	// not asked or could not be reached.
	OnAPI  *bool        `json:"onApi,omitempty"`
	Tech   project.Tech `json:"tech"`
	Git    *StatusGit   `json:"git,omitempty"`
	Readme string       `json:"readme,omitempty"`
	// Notes say what is off and the command that fixes it.
	Notes []string `json:"notes"`
}

// StatusGit is a repository at a glance. Ahead/behind are against the
// upstream as of the last fetch; status never fetches.
type StatusGit struct {
	Branch      string `json:"branch"` // "" when detached
	Detached    bool   `json:"detached,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
	Dirty       int    `json:"dirty"`
	Head        string `json:"head,omitempty"`
	Subject     string `json:"subject,omitempty"`
	CommittedAt string `json:"committedAt,omitempty"`
}

// StatusAPI is how the API's project list went: online, offline (with the
// error) or not logged in.
type StatusAPI struct {
	Host     string `json:"host"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
	Projects int    `json:"projects"`
}

const (
	APIOnline      = "online"
	APIOffline     = "offline"
	APINotLoggedIn = "not logged in"
)

// apiTimeout bounds the API's project list: status is a glance, and an
// unreachable API must not hold it up.
const apiTimeout = 2 * time.Second

// apiProjects lists the workspace's projects on gears-api. A var so tests
// can stand in for the API.
var apiProjects = func(ws string) (StatusAPI, []api.Project) {
	st := StatusAPI{Host: api.Host()}
	cl, err := api.Open(st.Host)
	if err != nil {
		st.State, st.Error = APIOffline, err.Error()
		if errors.Is(err, api.ErrNotLoggedIn) {
			st.State, st.Error = APINotLoggedIn, ""
		}
		return st, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	list, err := cl.ListProjects(ctx, ws)
	if err != nil {
		st.State, st.Error = APIOffline, err.Error()
		return st, nil
	}
	st.State, st.Projects = APIOnline, len(list)
	return st, list
}

// workspaceProjects builds the projects section. The API call and every
// project's git reads run at once; the slowest one sets the time.
func workspaceProjects(c Ctx) ([]StatusProject, StatusAPI, error) {
	found, err := project.Discover(c.Project)
	if err != nil {
		return nil, StatusAPI{}, err
	}
	var (
		wg      sync.WaitGroup
		apiSt   StatusAPI
		apiList []api.Project
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		apiSt, apiList = apiProjects(c.Project.WorkspaceID())
	}()
	out := make([]StatusProject, len(found))
	for i, f := range found {
		out[i] = StatusProject{ID: f.ID, Slug: f.Slug, Name: f.Slug, Path: f.Path, Remote: f.Remote,
			Listed: f.Listed, OnDisk: f.OnDisk, Notes: []string{}}
		if c.Current != nil && c.Current.Path == f.Path {
			out[i].Current = true
		}
		if !f.OnDisk {
			continue
		}
		wg.Add(1)
		go func(sp *StatusProject, f project.Found) {
			defer wg.Done()
			overview(sp, f)
		}(&out[i], f)
	}
	wg.Wait()

	for i := range out {
		sp := &out[i]
		switch {
		case !sp.OnDisk:
			sp.Notes = append(sp.Notes, "missing: in projects.yaml, not on disk")
		case !found[i].Git:
			sp.Notes = append(sp.Notes, "not a git repo")
		case !sp.Listed:
			sp.Notes = append(sp.Notes, "unlisted, run gears projects scan")
		}
	}

	if apiSt.State == APIOnline {
		matched := make([]bool, len(apiList))
		for i := range out {
			sp := &out[i]
			j := apiMatch(apiList, matched, sp)
			on := j >= 0
			if on {
				matched[j] = true
				if ap := apiList[j]; ap.Name != "" {
					sp.Name = ap.Name
				}
				if sp.Remote == "" {
					sp.Remote = apiList[j].GitRemote
				}
			}
			sp.OnAPI = &on
			if !on && sp.Listed {
				sp.Notes = append(sp.Notes, "not on the API yet; gears sync adds it")
			}
		}
		yes := true
		for j, ap := range apiList {
			if matched[j] {
				continue
			}
			name := ap.Name
			if name == "" {
				name = ap.Slug
			}
			out = append(out, StatusProject{ID: ap.ID, Slug: ap.Slug, Name: name, Remote: ap.GitRemote, OnAPI: &yes,
				Notes: []string{"on the API only: not cloned under projects/"}})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Path == "") != (out[j].Path == "") {
			return out[j].Path == ""
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Slug < out[j].Slug
	})
	return out, apiSt, nil
}

// apiMatch is the API project that is sp: the same id, else (an id the
// API has not adopted yet) the same remote. -1 when none.
func apiMatch(list []api.Project, taken []bool, sp *StatusProject) int {
	for j, ap := range list {
		if !taken[j] && sp.ID != "" && ap.ID == sp.ID {
			return j
		}
	}
	if sp.Remote == "" {
		return -1
	}
	for j, ap := range list {
		if !taken[j] && ap.GitRemote != "" && gitx.NormalizeRemote(ap.GitRemote) == gitx.NormalizeRemote(sp.Remote) {
			return j
		}
	}
	return -1
}

// overview fills tech, README and git for a project on disk. Tech and
// README are file reads; git is two commands plus the remote.
func overview(sp *StatusProject, f project.Found) {
	sp.Tech = project.DetectTech(f.Dir)
	sp.Readme = project.ReadmeLine(f.Dir)
	if !f.Git {
		return
	}
	var wg sync.WaitGroup
	var remote string
	wg.Add(1)
	go func() {
		defer wg.Done()
		remote = gitx.RemoteURL(f.Dir)
	}()
	if s, ok := gitx.Snapshot(f.Dir); ok {
		sp.Git = &StatusGit{Branch: s.Branch, Detached: s.Detached, Upstream: s.Upstream, Ahead: s.Ahead, Behind: s.Behind,
			Dirty: s.Dirty, Head: s.Head, Subject: s.Subject, CommittedAt: ts(s.At)}
	}
	wg.Wait()
	if remote != "" {
		sp.Remote = remote
	}
}
