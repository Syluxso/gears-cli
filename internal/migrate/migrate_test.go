package migrate

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/shape"
)

// v2Tree writes a synthetic gears v2 .gears under dir, shaped like what the
// v2 CLI's init, adr, story, session and hydrate commands leave behind.
func v2Tree(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		".gearbox/config.json":                     `{"workspace_id":"w-1","api_base_url":"http://x"}`,
		".gearbox/gears.db":                        "SQLite format 3\x00",
		".gearbox/hydrate.md":                      "# Hydrate\n",
		"index.md":                                 "# Acme workspace\n\nProjects: api, web.\n",
		"README.md":                                "# .gears\n\nv2 framework readme.\n",
		"gears-init.md":                            "# Gears init\n",
		"artifacts/index.md":                       "# Artifacts\n",
		"artifacts/adr--routing.md":                "# Route Organization Pattern\n\nPrefixes per realm.\n",
		"artifacts/adr_old-style.md":               "# Old style ADR\n\nBody.\n",
		"artifacts/adr--use-postgres.md":           "# Use Postgres (artifact)\n",
		"artifacts/adr_template.md":                "# Template\n",
		"artifacts/adr_example-routing.md":         "# Example\n",
		"artifacts/idea--dark-mode.md":             "# Dark mode\n",
		"artifacts/notes.md":                       "no heading here\n",
		"artifacts/diagram.png":                    "\x89PNG",
		"decisions/index.md":                       "# Architectural Decision Records\n",
		"decisions/use-postgres.md":                "## ADR-001: Use Postgres\n\n# Use Postgres\n",
		"story/index.md":                           "# Stories\n",
		"story/story--login.md":                    "---\nstatus: queued\n---\n# Login with email\n\nAs a user...\n",
		"sessions/index.md":                        "# Sessions\n",
		"sessions/2026-04-01.md":                   "# Session 2026-04-01\n\nDid things.\n",
		"sessions/hydrate-report-20260401-1000.md": "# Hydrate report\n",
		"memory/index.md":                          "# Memory\n",
		"memory/projects/api.md":                   "# API notes\n",
		"context/index.md":                         "# Context\n",
		"instructions/index.md":                    "---\ntitle: Coding standards\n---\nUse gofmt.\n",
	}
	for rel, body := range files {
		path := filepath.Join(dir, project.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// want is the mapping the synthetic tree must produce: v2 path -> v3 file
// ("" means skipped).
var want = map[string]string{
	".gearbox/":                                "",
	"index.md":                                 "index--migrated--workspace.md",
	"README.md":                                "",
	"gears-init.md":                            "",
	"artifacts/index.md":                       "index--migrated--artifacts.md",
	"artifacts/adr--routing.md":                "adr--migrated--routing.md",
	"artifacts/adr_old-style.md":               "adr--migrated--old-style.md",
	"artifacts/adr--use-postgres.md":           "adr--migrated--use-postgres.md",
	"artifacts/adr_template.md":                "",
	"artifacts/adr_example-routing.md":         "",
	"artifacts/idea--dark-mode.md":             "idea--migrated--dark-mode.md",
	"artifacts/notes.md":                       "free--migrated--notes.md",
	"artifacts/diagram.png":                    "",
	"decisions/index.md":                       "index--migrated--decisions.md",
	"decisions/use-postgres.md":                "adr--migrated--use-postgres-2.md",
	"story/index.md":                           "index--migrated--story.md",
	"story/story--login.md":                    "story--migrated--login.md",
	"sessions/index.md":                        "index--migrated--sessions.md",
	"sessions/2026-04-01.md":                   "free--migrated--sessions-2026-04-01.md",
	"sessions/hydrate-report-20260401-1000.md": "",
	"memory/index.md":                          "index--migrated--memory.md",
	"memory/projects/api.md":                   "free--migrated--memory-projects-api.md",
	"context/index.md":                         "index--migrated--context.md",
	"instructions/index.md":                    "index--migrated--instructions.md",
}

func checkMapping(t *testing.T, plan *Plan) {
	t.Helper()
	if len(plan.Items) != len(want) {
		t.Errorf("%d items, want %d: %+v", len(plan.Items), len(want), plan.Items)
	}
	for _, it := range plan.Items {
		w, ok := want[it.Source]
		if !ok {
			t.Errorf("unexpected item %+v", it)
			continue
		}
		if w == "" && it.Action != "skip" {
			t.Errorf("%s: want skip, got %+v", it.Source, it)
		}
		if w != "" && it.File != w {
			t.Errorf("%s: file %q, want %q", it.Source, it.File, w)
		}
	}
}

func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	v2Tree(t, dir)

	// Dry run: the plan is complete and nothing on disk changes.
	plan, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Rename || !plan.Zip || !plan.Init {
		t.Fatalf("fresh plan %+v", plan)
	}
	checkMapping(t, plan)
	if c, e, s := plan.Counts(); c != 17 || e != 0 || s != 7 {
		t.Fatalf("counts create %d exists %d skip %d", c, e, s)
	}
	for _, p := range []string{OldDir, ZipFile, filepath.Join(project.Dir, project.BindingFile)} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Fatalf("dry run touched %s", p)
		}
	}

	// Real run.
	res, err := Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, OldDir, ".gearbox", "gears.db")); err != nil {
		t.Fatalf(".gears_v2 not kept: %v", err)
	}
	p, err := project.Find(dir)
	if err != nil || p.Root != dir {
		t.Fatalf("v3 binding: %v %v", p, err)
	}
	if res.Project.ID != p.ID {
		t.Fatalf("result project %s, bound %s", res.Project.ID, p.ID)
	}
	// The old database and config are not carried into the v3 tree.
	if _, err := os.Stat(filepath.Join(dir, project.Dir, ".gearbox", "config.json")); !os.IsNotExist(err) {
		t.Fatal("v2 config copied into .gears")
	}

	// Zip holds the whole v2 tree under .gears_v2/.
	zr, err := zip.OpenReader(filepath.Join(dir, ZipFile))
	if err != nil {
		t.Fatal(err)
	}
	inZip := map[string]bool{}
	for _, f := range zr.File {
		inZip[f.Name] = true
	}
	zr.Close()
	for _, name := range []string{".gears_v2/.gearbox/gears.db", ".gears_v2/.gearbox/config.json", ".gears_v2/artifacts/adr--routing.md", ".gears_v2/artifacts/diagram.png", ".gears_v2/memory/projects/api.md"} {
		if !inZip[name] {
			t.Errorf("zip lacks %s (has %v)", name, inZip)
		}
	}

	// Artifacts: right names, adopted into the record, titled, body kept.
	list, err := artifact.List(p)
	if err != nil {
		t.Fatal(err)
	}
	byFile := map[string]artifact.Artifact{}
	for _, a := range list {
		byFile[a.File] = a
	}
	if len(byFile) != 17 {
		t.Fatalf("%d artifacts, want 17: %v", len(byFile), byFile)
	}
	titles := map[string]string{
		"adr--migrated--routing.md":              "Route Organization Pattern",
		"adr--migrated--use-postgres-2.md":       "Use Postgres",
		"story--migrated--login.md":              "Login with email",
		"free--migrated--notes.md":               "notes",
		"index--migrated--instructions.md":       "Coding standards",
		"free--migrated--sessions-2026-04-01.md": "Session 2026-04-01",
		"idea--migrated--dark-mode.md":           "Dark mode",
	}
	for file, title := range titles {
		a, ok := byFile[file]
		if !ok {
			t.Errorf("missing %s", file)
			continue
		}
		if a.Title != title {
			t.Errorf("%s title %q, want %q", file, a.Title, title)
		}
	}
	for _, it := range plan.Items {
		if it.Action == "skip" {
			continue
		}
		if prefix, _, ok := artifact.Parse(it.File); !ok || prefix != byFile[it.File].Prefix || prefix != it.Prefix {
			t.Errorf("%s: prefix %q / row %q", it.File, it.Prefix, byFile[it.File].Prefix)
		}
	}
	data, err := os.ReadFile(filepath.Join(p.ArtifactsDir(), "story--migrated--login.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := shape.Split(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if doc.FrontMatter["status"] != "queued" || doc.FrontMatter["date"] == nil || doc.FrontMatter["title"] != "Login with email" ||
		doc.FrontMatter["migrated_from"] != ".gears_v2/story/story--login.md" || !strings.Contains(doc.Body, "As a user...") {
		t.Fatalf("story front matter %v body %q", doc.FrontMatter, doc.Body)
	}

	// Re-run: nothing to rename, zip or init; every artifact already there.
	zipInfo, _ := os.Stat(filepath.Join(dir, ZipFile))
	again, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Rename || again.Zip || again.Init {
		t.Fatalf("re-run plan %+v", again)
	}
	checkMapping(t, again)
	if c, e, _ := again.Counts(); c != 0 || e != 17 {
		t.Fatalf("re-run counts create %d exists %d", c, e)
	}
	// A migrated file deleted since is the only one written again.
	if _, err := artifact.Delete(p, "free--migrated--notes.md"); err != nil {
		t.Fatal(err)
	}
	again, err = Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c, e, _ := again.Counts(); c != 1 || e != 16 {
		t.Fatalf("after delete: create %d exists %d", c, e)
	}
	if _, err := Run(again); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(dir, ZipFile)); !st.ModTime().Equal(zipInfo.ModTime()) || st.Size() != zipInfo.Size() {
		t.Fatal("re-run rewrote the zip")
	}
	if list, _ := artifact.List(p); len(list) != 17 {
		t.Fatalf("after re-run %d artifacts, want 17", len(list))
	}
}

func TestMigrateRefusals(t *testing.T) {
	// No .gears at all.
	if _, err := Detect(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no gears v2 workspace") {
		t.Fatalf("empty dir: %v", err)
	}
	// Already v3, never migrated.
	v3 := t.TempDir()
	if _, err := project.Init(v3, "ws", "WS"); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(v3); err == nil || !strings.Contains(err.Error(), "already a Gears v3 workspace") {
		t.Fatalf("v3 dir: %v", err)
	}
	// A v2 tree inside a v3 workspace.
	sub := filepath.Join(v3, "projects", "old")
	v2Tree(t, sub)
	if _, err := Detect(sub); err == nil || !strings.Contains(err.Error(), "inside the Gears v3 workspace at "+v3) {
		t.Fatalf("inside v3: %v", err)
	}
	// Both a v2 .gears and a .gears_v2.
	both := t.TempDir()
	v2Tree(t, both)
	if err := os.MkdirAll(filepath.Join(both, OldDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(both); err == nil || !strings.Contains(err.Error(), "move one aside") {
		t.Fatalf("both: %v", err)
	}
	// A .gears that is neither v3 nor recognisably v2.
	odd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(odd, project.Dir, "whatever"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(odd); err == nil || !strings.Contains(err.Error(), "does not look like a gears v2 tree") {
		t.Fatalf("odd: %v", err)
	}
}
