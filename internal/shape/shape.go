// Package shape defines what an artifact must contain. A shape is a small
// YAML document: the front matter keys that must be present, the "## "
// headings the body must carry, and a minimum body length. Built-in shapes
// are embedded; a project overrides one by placing shape--<prefix>.yaml in
// its artifacts directory.
package shape

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Shape is the schema for one artifact prefix.
type Shape struct {
	Prefix      string   `yaml:"prefix"`
	Title       string   `yaml:"title"`
	FrontMatter []string `yaml:"frontMatter"`
	Sections    []string `yaml:"sections"`
	MinBody     int      `yaml:"minBody"`
	// Source is the override file this shape came from; empty for built-in.
	Source string `yaml:"-"`
}

//go:embed defaults/*.yaml
var defaults embed.FS

// Set maps prefix to the shape in force.
type Set map[string]*Shape

// Load returns the built-in shapes with any shape--*.yaml in artifactsDir
// layered on top. A missing directory is not an error.
func Load(artifactsDir string) (Set, error) {
	set := Set{}

	entries, err := defaults.ReadDir("defaults")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		data, err := defaults.ReadFile("defaults/" + e.Name())
		if err != nil {
			return nil, err
		}
		s, err := parse(data)
		if err != nil {
			return nil, fmt.Errorf("built-in %s: %w", e.Name(), err)
		}
		set[s.Prefix] = s
	}

	overrides, err := filepath.Glob(filepath.Join(artifactsDir, "shape--*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, path := range overrides {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		s, err := parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		s.Source = filepath.Base(path)
		set[s.Prefix] = s
	}
	return set, nil
}

func parse(data []byte) (*Shape, error) {
	var s Shape
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Prefix == "" {
		return nil, fmt.Errorf("shape has no prefix")
	}
	return &s, nil
}

// Document is an artifact split into front matter and body.
type Document struct {
	FrontMatter map[string]any
	Body        string
}

// Split separates a leading YAML front matter block from the body. A file
// without front matter yields an empty map and the whole text as body.
func Split(text string) (Document, error) {
	doc := Document{FrontMatter: map[string]any{}}
	t := strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(t, "---\n") {
		doc.Body = t
		return doc, nil
	}
	rest := t[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return doc, fmt.Errorf("front matter is not closed with ---")
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &doc.FrontMatter); err != nil {
		return doc, fmt.Errorf("front matter: %w", err)
	}
	body := rest[end+4:]
	doc.Body = strings.TrimLeft(body, "\n")
	return doc, nil
}

// Validate returns one message per rule the document breaks. Empty means it
// passes. Rules: every front matter key present and non-empty; every section
// heading present as "## <name>"; body (minus headings) at least MinBody.
func (s *Shape) Validate(doc Document) []string {
	var errs []string
	for _, key := range s.FrontMatter {
		v, ok := doc.FrontMatter[key]
		if !ok || isEmpty(v) {
			errs = append(errs, "front matter missing: "+key)
		}
	}
	headings := map[string]bool{}
	var prose strings.Builder
	for _, line := range strings.Split(doc.Body, "\n") {
		if strings.HasPrefix(line, "## ") {
			headings[strings.TrimSpace(line[3:])] = true
			continue
		}
		prose.WriteString(strings.TrimSpace(line))
	}
	for _, name := range s.Sections {
		if !headings[name] {
			errs = append(errs, "section missing: ## "+name)
		}
	}
	if n := len(prose.String()); s.MinBody > 0 && n < s.MinBody {
		errs = append(errs, fmt.Sprintf("body too short: %d chars, shape requires %d", n, s.MinBody))
	}
	return errs
}

// Seed renders an empty document that satisfies the shape's structure, for
// an agent to fill in. Front matter values come from the given map; keys
// the shape requires but the map lacks are written empty.
func (s *Shape) Seed(front map[string]string) string {
	var b strings.Builder
	if len(s.FrontMatter) > 0 || len(front) > 0 {
		b.WriteString("---\n")
		seen := map[string]bool{}
		for _, key := range s.FrontMatter {
			seen[key] = true
			fmt.Fprintf(&b, "%s: %s\n", key, quote(front[key]))
		}
		for k, v := range front {
			if !seen[k] {
				fmt.Fprintf(&b, "%s: %s\n", k, quote(v))
			}
		}
		b.WriteString("---\n\n")
	}
	if title := front["title"]; title != "" {
		fmt.Fprintf(&b, "# %s\n\n", title)
	}
	for _, name := range s.Sections {
		fmt.Fprintf(&b, "## %s\n\n\n", name)
	}
	return b.String()
}

func quote(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, ":#'\"\n") {
		return fmt.Sprintf("%q", v)
	}
	return v
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	default:
		return false
	}
}
