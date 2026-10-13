package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-40: a command run inside projects/<repo> resolves to the root binding
// and tags session entries and new artifacts with that project's id; at the
// root they stay workspace-level. Artifacts all land in the root's
// .gears/artifacts/, and a file name is unique across the workspace.
func TestCurrentProjectAttribution(t *testing.T) {
	root := t.TempDir()
	res, err := project.Init(root, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	const appID = "01a133a5-5b60-74eb-812a-71bb593e0b71"
	m := &project.Manifest{Projects: []project.ManifestEntry{
		{ID: appID, Slug: "gears-app", Path: "projects/gears-app", Remote: "https://github.com/Syluxso/gears-app.git"},
		{ID: "01a12688-563c-7dda-a360-db460f196103", Slug: "gears-cli", Path: "projects/gears-cli"},
	}}
	if err := m.Save(res.Project); err != nil {
		t.Fatal(err)
	}
	inApp := filepath.Join(root, "projects", "gears-app", "src")
	if err := os.MkdirAll(inApp, 0o755); err != nil {
		t.Fatal(err)
	}

	c, err := ctxAt(inApp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Project.Root != root || c.Current == nil || c.Current.ID != appID {
		t.Fatalf("ctx in app: root %s current %+v", c.Project.Root, c.Current)
	}
	e, err := verbs.SessionNew(c, verbs.SessionNewIn{Kind: "done", Text: "app work"})
	if err != nil {
		t.Fatal(err)
	}
	if e.ProjectID != appID {
		t.Fatalf("session entry project %q, want %s", e.ProjectID, appID)
	}
	a, err := verbs.ArtifactNew(c, verbs.ArtifactNewIn{Prefix: "free", Title: "App note"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ProjectID != appID || filepath.Dir(a.Path) != res.Project.ArtifactsDir() {
		t.Fatalf("artifact %+v", a)
	}
	// The same name from the root is refused, not a second file.
	rc, err := ctxAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Current != nil {
		t.Fatalf("root has a current project: %+v", rc.Current)
	}
	if _, err := verbs.ArtifactNew(rc, verbs.ArtifactNewIn{Prefix: "free", Title: "App note"}); err == nil {
		t.Fatal("duplicate artifact name across the workspace was accepted")
	}
	w, err := verbs.SessionNew(rc, verbs.SessionNewIn{Kind: "done", Text: "workspace work"})
	if err != nil {
		t.Fatal(err)
	}
	if w.ProjectID != "" {
		t.Fatalf("root entry tagged %q", w.ProjectID)
	}
	// An explicit project from the root, by slug.
	x, err := verbs.SessionNew(rc, verbs.SessionNewIn{Kind: "done", Text: "cli work", Project: "gears-cli"})
	if err != nil || x.ProjectID != "01a12688-563c-7dda-a360-db460f196103" {
		t.Fatalf("explicit project: %+v %v", x, err)
	}
	if _, err := verbs.SessionNew(rc, verbs.SessionNewIn{Kind: "done", Text: "nope", Project: "nope"}); err == nil {
		t.Fatal("unknown project accepted")
	}

	// What was written reads back with its project.
	list, err := verbs.SessionList(rc, verbs.SessionListIn{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range list.Entries {
		got[l.Text] = l.ProjectID
	}
	if got["app work"] != appID || got["workspace work"] != "" {
		t.Fatalf("listed: %v", got)
	}
	arts, err := artifact.List(res.Project)
	if err != nil || len(arts) != 1 || arts[0].ProjectID != appID {
		t.Fatalf("artifacts: %+v %v", arts, err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", "gears-app", project.Dir)); !os.IsNotExist(err) {
		t.Fatalf("a .gears appeared under projects/: %v", err)
	}
}
