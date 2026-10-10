// Package artifact manages the prefixed files in .gears/artifacts/: naming,
// creation from a shape, listing, and validation. Sync to the API is not
// here yet; this is the local half.
package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Syluxso/gears/internal/project"
	"github.com/Syluxso/gears/internal/shape"
)

// Prefixes in display order. "shape" files live beside these but are schemas,
// not artifacts, so they are not listed.
var Prefixes = []string{"adr", "story", "idea", "index", "free"}

// Artifact is one file in the artifacts directory.
type Artifact struct {
	Prefix string
	Slug   string
	File   string // base name
	Path   string // absolute
}

const sep = "--"

func isPrefix(p string) bool {
	for _, q := range Prefixes {
		if q == p {
			return true
		}
	}
	return false
}

// Parse splits "<prefix>--<slug>.md" into its parts; ok is false for files
// that are not artifacts (shapes, conflicts, stray files).
func Parse(file string) (prefix, slug string, ok bool) {
	if !strings.HasSuffix(file, ".md") {
		return "", "", false
	}
	name := strings.TrimSuffix(file, ".md")
	i := strings.Index(name, sep)
	if i <= 0 {
		return "", "", false
	}
	prefix, slug = name[:i], name[i+len(sep):]
	if !isPrefix(prefix) || slug == "" {
		return "", "", false
	}
	return prefix, slug, true
}

// List returns every artifact in the project, sorted by prefix order then
// name. A missing directory yields an empty list.
func List(p *project.Project) ([]Artifact, error) {
	entries, err := os.ReadDir(p.ArtifactsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Artifact
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		prefix, slug, ok := Parse(e.Name())
		if !ok {
			continue
		}
		out = append(out, Artifact{
			Prefix: prefix,
			Slug:   slug,
			File:   e.Name(),
			Path:   filepath.Join(p.ArtifactsDir(), e.Name()),
		})
	}
	rank := map[string]int{}
	for i, pre := range Prefixes {
		rank[pre] = i
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Prefix != out[j].Prefix {
			return rank[out[i].Prefix] < rank[out[j].Prefix]
		}
		return out[i].File < out[j].File
	})
	return out, nil
}

// New creates <prefix>--<slug>.md seeded from the prefix's shape. ADRs are
// numbered (adr--001-...) so their order is visible in a listing.
func New(p *project.Project, prefix, title string) (*Artifact, error) {
	if !isPrefix(prefix) {
		return nil, fmt.Errorf("unknown prefix %q; one of %s", prefix, strings.Join(Prefixes, ", "))
	}
	slug := project.Slugify(title)
	if slug == "" {
		return nil, fmt.Errorf("title %q produces an empty slug", title)
	}
	if prefix == "adr" {
		n, err := nextADRNumber(p)
		if err != nil {
			return nil, err
		}
		slug = fmt.Sprintf("%03d-%s", n, slug)
	}

	set, err := shape.Load(p.ArtifactsDir())
	if err != nil {
		return nil, err
	}
	s, ok := set[prefix]
	if !ok {
		return nil, fmt.Errorf("no shape for prefix %q", prefix)
	}

	if err := os.MkdirAll(p.ArtifactsDir(), 0o755); err != nil {
		return nil, err
	}
	file := prefix + sep + slug + ".md"
	path := filepath.Join(p.ArtifactsDir(), file)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s already exists", file)
	}

	front := map[string]string{
		"title":  title,
		"date":   time.Now().Format("2006-01-02"),
		"status": "draft",
	}
	// Only pass keys the shape asks for; Seed writes extras otherwise.
	wanted := map[string]string{}
	for _, k := range s.FrontMatter {
		wanted[k] = front[k]
	}
	if err := os.WriteFile(path, []byte(s.Seed(wanted)), 0o644); err != nil {
		return nil, err
	}
	return &Artifact{Prefix: prefix, Slug: slug, File: file, Path: path}, nil
}

func nextADRNumber(p *project.Project) (int, error) {
	list, err := List(p)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range list {
		if a.Prefix != "adr" {
			continue
		}
		var k int
		if _, err := fmt.Sscanf(a.Slug, "%03d-", &k); err == nil && k > n {
			n = k
		}
	}
	return n + 1, nil
}

// Validate checks one artifact against the shape in force for its prefix.
// A read or parse failure is reported as a single message.
func Validate(p *project.Project, a Artifact) []string {
	set, err := shape.Load(p.ArtifactsDir())
	if err != nil {
		return []string{err.Error()}
	}
	s, ok := set[a.Prefix]
	if !ok {
		return []string{"no shape for prefix " + a.Prefix}
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return []string{err.Error()}
	}
	doc, err := shape.Split(string(data))
	if err != nil {
		return []string{err.Error()}
	}
	return s.Validate(doc)
}
