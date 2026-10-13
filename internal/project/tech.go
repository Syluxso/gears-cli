package project

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Tech is what a project is built with, read from the files at its root.
// The rules are the retired v2 hydrate's (Syluxso/gears cmd/hydrate.go,
// detectProjectStack), widened: the first marker in this order wins, so a
// Laravel app with a package.json is PHP, not Node.
type Tech struct {
	// Kind is the stack: "Go", "Node/Angular", "PHP/Laravel", "Python",
	// "Rust", ".NET", "Java/Maven", …; "" when nothing is recognised and
	// "empty" when the directory holds nothing but .git.
	Kind string `json:"kind"`
	// Detail is one extra fact when the marker carries it: the Go module
	// path, the package or crate name.
	Detail string `json:"detail,omitempty"`
}

func (t Tech) String() string {
	if t.Kind == "" {
		return "unknown"
	}
	return t.Kind
}

// DetectTech reads markers at dir's top level only; it never walks the tree.
func DetectTech(dir string) Tech {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		return Tech{Kind: "Go", Detail: goModule(filepath.Join(dir, "go.mod"))}
	case has("Cargo.toml"):
		return Tech{Kind: "Rust", Detail: tomlName(filepath.Join(dir, "Cargo.toml"))}
	case has("composer.json"):
		return composerTech(dir, has("artisan"))
	case has("package.json"):
		return nodeTech(filepath.Join(dir, "package.json"))
	case has("pyproject.toml"):
		return Tech{Kind: "Python", Detail: tomlName(filepath.Join(dir, "pyproject.toml"))}
	case has("requirements.txt"), has("setup.py"), has("Pipfile"):
		return Tech{Kind: "Python"}
	case glob(dir, "*.csproj", "*.fsproj", "*.sln"):
		return Tech{Kind: ".NET"}
	case has("pom.xml"):
		return Tech{Kind: "Java/Maven"}
	case has("build.gradle.kts"), has("build.gradle"):
		return Tech{Kind: "Java/Gradle"}
	case has("Gemfile"):
		return Tech{Kind: "Ruby"}
	case has("mix.exs"):
		return Tech{Kind: "Elixir"}
	case has("pubspec.yaml"):
		return Tech{Kind: "Dart/Flutter"}
	case has("Package.swift"):
		return Tech{Kind: "Swift"}
	case has("CMakeLists.txt"):
		return Tech{Kind: "C/C++"}
	case has("Dockerfile"):
		return Tech{Kind: "Docker"}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != ".git" {
			return Tech{}
		}
	}
	return Tech{Kind: "empty"}
}

func glob(dir string, patterns ...string) bool {
	for _, p := range patterns {
		if m, _ := filepath.Glob(filepath.Join(dir, p)); len(m) > 0 {
			return true
		}
	}
	return false
}

// goModule is the path on go.mod's module line.
func goModule(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// tomlName is the first name = "…" line ([package] in Cargo, [project] in
// pyproject); good enough without a TOML parser.
func tomlName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) == "name" {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func composerTech(dir string, artisan bool) Tech {
	var c struct {
		Name    string            `json:"name"`
		Require map[string]string `json:"require"`
	}
	data, _ := os.ReadFile(filepath.Join(dir, "composer.json"))
	_ = json.Unmarshal(data, &c)
	t := Tech{Kind: "PHP", Detail: c.Name}
	switch {
	case artisan || c.Require["laravel/framework"] != "":
		t.Kind = "PHP/Laravel"
	case c.Require["symfony/framework-bundle"] != "":
		t.Kind = "PHP/Symfony"
	}
	return t
}

// nodeFrameworks is checked in order: a meta-framework before the library
// under it (Next before React, Ionic before Angular).
var nodeFrameworks = []struct{ dep, kind string }{
	{"next", "Node/Next.js"},
	{"nuxt", "Node/Nuxt"},
	{"@remix-run/react", "Node/Remix"},
	{"@sveltejs/kit", "Node/SvelteKit"},
	{"astro", "Node/Astro"},
	{"@ionic/angular", "Node/Ionic"},
	{"@angular/core", "Node/Angular"},
	{"electron", "Node/Electron"},
	{"react-native", "Node/React Native"},
	{"react", "Node/React"},
	{"vue", "Node/Vue"},
	{"svelte", "Node/Svelte"},
	{"@nestjs/core", "Node/NestJS"},
	{"express", "Node/Express"},
}

func nodeTech(path string) Tech {
	var pkg struct {
		Name            string            `json:"name"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &pkg)
	t := Tech{Kind: "Node", Detail: pkg.Name}
	for _, f := range nodeFrameworks {
		if pkg.Dependencies[f.dep] != "" || pkg.DevDependencies[f.dep] != "" {
			t.Kind = f.kind
			break
		}
	}
	return t
}

// ReadmeLine is what the project says it is: the first sentence of the
// first prose paragraph of its README (headings, badges, images, HTML and
// code skipped), else the first heading, else "". Markdown is left as
// written; at most 200 runes.
func ReadmeLine(dir string) string {
	var data []byte
	for _, name := range []string{"README.md", "readme.md", "Readme.md", "README", "README.txt", "README.rst"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			data = b
			break
		}
	}
	if data == nil {
		return ""
	}
	heading := ""
	inFence := false
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i, line := range lines {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			inFence = !inFence
			continue
		}
		switch {
		case inFence, l == "", strings.HasPrefix(l, "<"), strings.HasPrefix(l, "[!["), strings.HasPrefix(l, "!["),
			strings.HasPrefix(l, "---"), strings.HasPrefix(l, "==="), strings.HasPrefix(l, "|"):
			continue
		case strings.HasPrefix(l, "#"):
			if heading == "" {
				heading = strings.TrimSpace(strings.TrimLeft(l, "#"))
			}
			continue
		}
		// The paragraph this line opens, up to its first sentence.
		para := l
		for _, next := range lines[i+1:] {
			n := strings.TrimSpace(next)
			if n == "" || strings.HasPrefix(n, "#") || strings.HasPrefix(n, "```") {
				break
			}
			para += " " + n
		}
		if end := strings.Index(para, ". "); end > 0 {
			para = para[:end+1]
		}
		return clip(para, 200)
	}
	return clip(heading, 200)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
