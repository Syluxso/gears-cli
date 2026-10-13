package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// GRS-46: tech is read from root markers, first match wins, in the v2
// hydrate's order widened to more stacks.
func TestDetectTech(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  Tech
	}{
		{"go", map[string]string{"go.mod": "module github.com/x/y\n\ngo 1.26\n"}, Tech{"Go", "github.com/x/y"}},
		{"go beats node", map[string]string{"go.mod": "module m\n", "package.json": `{}`}, Tech{"Go", "m"}},
		{"rust", map[string]string{"Cargo.toml": "[package]\nname = \"crab\"\n"}, Tech{"Rust", "crab"}},
		{"laravel by artisan", map[string]string{"composer.json": `{"name":"a/b"}`, "artisan": "", "package.json": `{}`}, Tech{"PHP/Laravel", "a/b"}},
		{"laravel by require", map[string]string{"composer.json": `{"require":{"laravel/framework":"^11"}}`}, Tech{"PHP/Laravel", ""}},
		{"symfony", map[string]string{"composer.json": `{"require":{"symfony/framework-bundle":"7"}}`}, Tech{"PHP/Symfony", ""}},
		{"php", map[string]string{"composer.json": `{}`}, Tech{"PHP", ""}},
		{"next", map[string]string{"package.json": `{"name":"front","dependencies":{"next":"15","react":"19"}}`}, Tech{"Node/Next.js", "front"}},
		{"angular", map[string]string{"package.json": `{"dependencies":{"@angular/core":"20"}}`}, Tech{"Node/Angular", ""}},
		{"ionic", map[string]string{"package.json": `{"dependencies":{"@angular/core":"20","@ionic/angular":"8"}}`}, Tech{"Node/Ionic", ""}},
		{"react dev dep", map[string]string{"package.json": `{"devDependencies":{"react":"19"}}`}, Tech{"Node/React", ""}},
		{"vue", map[string]string{"package.json": `{"dependencies":{"vue":"3"}}`}, Tech{"Node/Vue", ""}},
		{"electron", map[string]string{"package.json": `{"devDependencies":{"electron":"30","react":"19"}}`}, Tech{"Node/Electron", ""}},
		{"plain node", map[string]string{"package.json": `{"name":"tool"}`}, Tech{"Node", "tool"}},
		{"bad package.json", map[string]string{"package.json": `{`}, Tech{"Node", ""}},
		{"pyproject", map[string]string{"pyproject.toml": "[project]\nname = \"snake\"\n"}, Tech{"Python", "snake"}},
		{"requirements", map[string]string{"requirements.txt": "flask\n"}, Tech{"Python", ""}},
		{"dotnet", map[string]string{"App.csproj": "<Project/>"}, Tech{".NET", ""}},
		{"maven", map[string]string{"pom.xml": "<project/>"}, Tech{"Java/Maven", ""}},
		{"gradle", map[string]string{"build.gradle.kts": ""}, Tech{"Java/Gradle", ""}},
		{"ruby", map[string]string{"Gemfile": ""}, Tech{"Ruby", ""}},
		{"flutter", map[string]string{"pubspec.yaml": ""}, Tech{"Dart/Flutter", ""}},
		{"docker only", map[string]string{"Dockerfile": "FROM scratch\n"}, Tech{"Docker", ""}},
		{"unknown", map[string]string{"notes.txt": "hi"}, Tech{"", ""}},
		{"empty", map[string]string{".git/HEAD": "ref: refs/heads/main\n"}, Tech{"empty", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				path := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := DetectTech(dir); got != tc.want {
				t.Fatalf("DetectTech = %+v, want %+v", got, tc.want)
			}
		})
	}
	if s := (Tech{}).String(); s != "unknown" {
		t.Fatalf("zero Tech prints %q", s)
	}
}

func TestReadmeLine(t *testing.T) {
	cases := []struct{ body, want string }{
		{"# gears-api\n\nThe plane behind Gears.\nMore.\n", "The plane behind Gears."},
		{"[![ci](x)](y)\n<p>html</p>\n# Title\n\n```\ncode\n```\nProse here\n", "Prose here"},
		{"# Only a heading\n\n## And another\n", "Only a heading"},
		{"# x\r\n\r\nThe plane behind Gears: the record every door\r\ntalks to. Second sentence.\r\n\r\nNext para.\r\n", "The plane behind Gears: the record every door talks to."},
		{"", ""},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ReadmeLine(dir); got != tc.want {
			t.Errorf("ReadmeLine(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
	if got := ReadmeLine(t.TempDir()); got != "" {
		t.Errorf("no README: %q", got)
	}
}

// Discover is read-only: listed entries, unlisted directories under
// projects/ and missing entries, without writing the manifest.
func TestDiscover(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	res, err := Init(root, "ws", "WS")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	gitRepo(t, filepath.Join(root, "projects", "alpha"), "git@github.com:example/alpha.git")
	gitRepo(t, filepath.Join(root, "projects", "gone"), "")
	if _, err := Scan(p, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "projects", "gone")); err != nil {
		t.Fatal(err)
	}
	gitRepo(t, filepath.Join(root, "projects", "beta"), "")
	if err := os.MkdirAll(filepath.Join(root, "projects", "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p.ManifestPath())

	found, err := Discover(p)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		path                string
		listed, disk, isGit bool
	}
	want := []row{
		{"projects/alpha", true, true, true},
		{"projects/beta", false, true, true},
		{"projects/gone", true, false, false},
		{"projects/notes", false, true, false},
	}
	if len(found) != len(want) {
		t.Fatalf("found %d: %+v", len(found), found)
	}
	for i, w := range want {
		f := found[i]
		if (row{f.Path, f.Listed, f.OnDisk, f.Git}) != w {
			t.Errorf("found[%d] = %+v, want %+v", i, f, w)
		}
	}
	if found[0].ID == "" || found[0].Remote != "git@github.com:example/alpha.git" {
		t.Errorf("alpha lost its manifest fields: %+v", found[0])
	}
	after, _ := os.ReadFile(p.ManifestPath())
	if string(before) != string(after) {
		t.Fatalf("Discover wrote the manifest")
	}
}
