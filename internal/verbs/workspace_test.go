package verbs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubAPI stands in for gears-api's project list for one test.
func stubAPI(t *testing.T, st StatusAPI, list []api.Project) {
	t.Helper()
	old := apiProjects
	apiProjects = func(string) (StatusAPI, []api.Project) { return st, list }
	t.Cleanup(func() { apiProjects = old })
}

// workspace builds a workspace root with projects/alpha: a Go repo with a
// README, pushed to a bare origin, then one commit ahead and two dirty
// files. projects/beta is a repo the manifest does not list yet.
func workspace(t *testing.T) (root string, p *project.Project, alphaID string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root = t.TempDir()
	res, err := project.Init(root, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p = res.Project
	origin := filepath.Join(t.TempDir(), "alpha.git")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "init", "-q", "--bare")
	alpha := filepath.Join(root, "projects", "alpha")
	if err := os.MkdirAll(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, alpha, "init", "-q")
	git(t, alpha, "symbolic-ref", "HEAD", "refs/heads/main")
	git(t, alpha, "remote", "add", "origin", origin)
	write(t, filepath.Join(alpha, "go.mod"), "module example.com/alpha\n\ngo 1.26\n")
	write(t, filepath.Join(alpha, "README.md"), "# alpha\n\nAlpha does the thing.\n")
	git(t, alpha, "add", ".")
	git(t, alpha, "commit", "-q", "-m", "first")
	git(t, alpha, "push", "-q", "-u", "origin", "main")
	write(t, filepath.Join(alpha, "main.go"), "package main\n")
	git(t, alpha, "add", ".")
	git(t, alpha, "commit", "-q", "-m", "second: add main")
	write(t, filepath.Join(alpha, "main.go"), "package main\n\nfunc main() {}\n")
	write(t, filepath.Join(alpha, "new.txt"), "untracked\n")

	if _, err := project.Scan(p, nil); err != nil {
		t.Fatal(err)
	}
	beta := filepath.Join(root, "projects", "beta")
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, beta, "init", "-q")
	write(t, filepath.Join(beta, "package.json"), `{"dependencies":{"@angular/core":"20"}}`)

	m, err := project.LoadManifest(p)
	if err != nil || len(m.Projects) != 1 {
		t.Fatalf("manifest: %v %+v", err, m)
	}
	return root, p, m.Projects[0].ID
}

// GRS-46: status reports every project with tech, remote, git overview and
// README; marks the current one; flags unlisted repos without writing the
// manifest; merges the API's list when online.
func TestStatusWorkspace(t *testing.T) {
	root, p, alphaID := workspace(t)
	stubAPI(t, StatusAPI{Host: "http://api.test", State: APIOnline, Projects: 2}, []api.Project{
		{ID: alphaID, Slug: "alpha", Name: "Alpha"},
		{ID: "01-api-only", Slug: "gamma", GitRemote: "git@github.com:example/gamma.git"},
	})
	before, _ := os.ReadFile(p.ManifestPath())

	alphaDir := filepath.Join(root, "projects", "alpha")
	start := time.Now()
	out, err := Status(Ctx{Project: p, Agent: "tester", Current: project.Current(p, alphaDir)}, StatusIn{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status took %s", time.Since(start))

	if after, _ := os.ReadFile(p.ManifestPath()); string(after) != string(before) {
		t.Fatal("status wrote the manifest")
	}
	if !out.IsWorkspace || out.API.State != APIOnline {
		t.Fatalf("header: workspace %v api %+v", out.IsWorkspace, out.API)
	}
	if len(out.Projects) != 3 {
		t.Fatalf("projects: %+v", out.Projects)
	}
	a, b, g := out.Projects[0], out.Projects[1], out.Projects[2]

	if a.Slug != "alpha" || a.Name != "Alpha" || !a.Current || !a.Listed || !a.OnDisk || a.OnAPI == nil || !*a.OnAPI {
		t.Errorf("alpha identity: %+v", a)
	}
	if a.Tech != (project.Tech{Kind: "Go", Detail: "example.com/alpha"}) || a.Readme != "Alpha does the thing." {
		t.Errorf("alpha tech/readme: %+v %q", a.Tech, a.Readme)
	}
	if !strings.HasSuffix(filepath.ToSlash(a.Remote), "alpha.git") {
		t.Errorf("alpha remote %q", a.Remote)
	}
	gi := a.Git
	if gi == nil || gi.Branch != "main" || gi.Upstream != "origin/main" || gi.Ahead != 1 || gi.Behind != 0 || gi.Dirty != 2 ||
		gi.Subject != "second: add main" || len(gi.Head) < 7 || gi.CommittedAt == "" {
		t.Errorf("alpha git: %+v", gi)
	}
	if len(a.Notes) != 0 {
		t.Errorf("alpha notes: %v", a.Notes)
	}

	if b.Slug != "beta" || b.Listed || b.Current || b.Tech.Kind != "Node/Angular" || b.Git == nil || b.Git.Head != "" {
		t.Errorf("beta: %+v git %+v", b, b.Git)
	}
	if len(b.Notes) == 0 || b.Notes[0] != "unlisted, run gears projects scan" {
		t.Errorf("beta notes: %v", b.Notes)
	}
	if b.OnAPI == nil || *b.OnAPI {
		t.Errorf("beta onApi: %v", b.OnAPI)
	}

	if g.Slug != "gamma" || g.Path != "" || g.OnDisk || g.OnAPI == nil || !*g.OnAPI || len(g.Notes) != 1 {
		t.Errorf("api-only gamma: %+v", g)
	}

	// The JSON the MCP tool and --json return.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"projects", "api", "isWorkspace", "desk", "cards", "runs", "artifacts", "sessionToday", "nextAction"} {
		if _, ok := shape[k]; !ok {
			t.Errorf("status JSON has no %q", k)
		}
	}
	p0 := shape["projects"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "slug", "name", "path", "remote", "current", "listed", "onDisk", "onApi", "tech", "git", "readme", "notes"} {
		if _, ok := p0[k]; !ok {
			t.Errorf("project JSON has no %q: %s", k, raw)
		}
	}
	g0 := p0["git"].(map[string]any)
	for _, k := range []string{"branch", "upstream", "ahead", "behind", "dirty", "head", "subject", "committedAt"} {
		if _, ok := g0[k]; !ok {
			t.Errorf("git JSON has no %q", k)
		}
	}
	if tech := p0["tech"].(map[string]any); tech["kind"] != "Go" || tech["detail"] != "example.com/alpha" {
		t.Errorf("tech JSON: %v", tech)
	}
}

// Offline (or logged out), status still reports the workspace from disk,
// says so once, and asserts nothing about the API per project.
func TestStatusWorkspaceOffline(t *testing.T) {
	_, p, _ := workspace(t)
	stubAPI(t, StatusAPI{Host: "http://api.test", State: APIOffline, Error: "unreachable"}, nil)
	out, err := Status(Ctx{Project: p, Agent: "tester"}, StatusIn{})
	if err != nil {
		t.Fatal(err)
	}
	if out.API.State != APIOffline || len(out.Projects) != 2 {
		t.Fatalf("api %+v projects %+v", out.API, out.Projects)
	}
	for _, sp := range out.Projects {
		if sp.OnAPI != nil || sp.Current {
			t.Errorf("%s: onApi %v current %v", sp.Slug, sp.OnAPI, sp.Current)
		}
	}
}
