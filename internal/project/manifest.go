package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Syluxso/gears-cli/internal/gitx"
)

// A workspace root is the only directory with .gears/ (decided 2026-10-12,
// GRS-40). The git repos under projects/ have no binding of their own; the
// root records them in .gears/projects.yaml. Like everything under .gears/
// the manifest is a cache: gears-api's project list is the authority, and
// sync makes every non-missing entry exist there under the entry's id.
const (
	ManifestFile = "projects.yaml"
	// ProjectsDir is where a workspace keeps its repos: <root>/projects/<repo>.
	ProjectsDir = "projects"
)

// ManifestEntry is one project in the workspace.
type ManifestEntry struct {
	ID   string `yaml:"id" json:"id"`
	Slug string `yaml:"slug" json:"slug"`
	// Path is relative to the workspace root, forward slashes: projects/gears-cli.
	Path   string `yaml:"path" json:"path"`
	Remote string `yaml:"remote" json:"remote"`
	// Missing is set when a scan no longer finds the repo. The entry is kept
	// (its id may still be on rows and on the API); it is never silently
	// dropped, and sync leaves it alone.
	Missing bool `yaml:"missing,omitempty" json:"missing,omitempty"`
}

// Manifest is .gears/projects.yaml.
type Manifest struct {
	Projects []ManifestEntry `yaml:"projects" json:"projects"`
}

func (p *Project) ManifestPath() string { return filepath.Join(p.GearsDir(), ManifestFile) }

// HasManifest reports whether .gears/projects.yaml exists.
func (p *Project) HasManifest() bool {
	_, err := os.Stat(p.ManifestPath())
	return err == nil
}

// LoadManifest reads .gears/projects.yaml; no file is an empty manifest.
func LoadManifest(p *Project) (*Manifest, error) {
	m := &Manifest{}
	data, err := os.ReadFile(p.ManifestPath())
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("%s: %w", p.ManifestPath(), err)
	}
	return m, nil
}

// Save writes the manifest, sorted by path so diffs stay readable.
func (m *Manifest) Save(p *Project) error {
	sort.SliceStable(m.Projects, func(i, j int) bool { return m.Projects[i].Path < m.Projects[j].Path })
	if m.Projects == nil {
		m.Projects = []ManifestEntry{}
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.GearsDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p.ManifestPath(), out, 0o644)
}

// ByID returns the entry with this id, or nil.
func (m *Manifest) ByID(id string) *ManifestEntry {
	for i := range m.Projects {
		if m.Projects[i].ID == id {
			return &m.Projects[i]
		}
	}
	return nil
}

// Lookup finds an entry by id, slug or path.
func (m *Manifest) Lookup(key string) *ManifestEntry {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	for i := range m.Projects {
		e := &m.Projects[i]
		if e.ID == key || e.Slug == key || e.Path == filepath.ToSlash(key) {
			return e
		}
	}
	return nil
}

// Current is the project a directory is in: the manifest entry whose path
// contains dir (at a workspace root), or the binding itself when it is a
// project (a standalone clone bound by remote). nil at the workspace root
// itself and anywhere outside projects/.
func Current(p *Project, dir string) *ManifestEntry {
	if !p.IsWorkspace() {
		return &ManifestEntry{ID: p.ID, Slug: p.Slug, Path: "."}
	}
	m, err := LoadManifest(p)
	if err != nil || len(m.Projects) == 0 {
		return nil
	}
	rel, err := filepath.Rel(p.Root, dir)
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	var best *ManifestEntry
	for i := range m.Projects {
		e := &m.Projects[i]
		if rel == e.Path || strings.HasPrefix(rel, e.Path+"/") {
			if best == nil || len(e.Path) > len(best.Path) {
				best = e
			}
		}
	}
	return best
}

// Lookup is how a scan asks gears-api which project already has a remote.
// ok is false when the API does not know it, or cannot be asked (offline,
// not logged in): the scan then mints an id and sync reconciles later.
type RemoteLookup func(remote string) (id string, ok bool)

// ScanResult is what a scan changed.
type ScanResult struct {
	Manifest *Manifest
	Added    []string // paths
	Missing  []string // paths newly marked missing
	Returned []string // paths that were missing and are back
	Adopted  []string // "path: old -> new" ids taken from the API
	Updated  []string // paths whose remote changed
}

// Changed reports whether the scan touched the manifest.
func (r *ScanResult) Changed() bool {
	return len(r.Added)+len(r.Missing)+len(r.Returned)+len(r.Adopted)+len(r.Updated) > 0
}

// Scan looks for git repos at <root>/projects/*/ and records them in the
// manifest. A repo is any directory with a .git (directory or file); one
// with no commits or no origin is still a project, with an empty remote.
// Known paths keep their id; a new path takes the id the API already holds
// for its remote, else the id of a missing entry with the same remote (the
// repo moved), else a fresh UUID v7. Entries whose repo is gone are marked
// missing, never dropped. Only a workspace root scans.
func Scan(p *Project, lookup RemoteLookup) (*ScanResult, error) {
	if !p.IsWorkspace() {
		return nil, fmt.Errorf("%s is a project binding, not a workspace root; projects are scanned at the workspace root", p.Root)
	}
	m, err := LoadManifest(p)
	if err != nil {
		return nil, err
	}
	res := &ScanResult{Manifest: m}
	found := map[string]string{} // path -> remote
	entries, _ := os.ReadDir(filepath.Join(p.Root, ProjectsDir))
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(p.Root, ProjectsDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue
		}
		path := ProjectsDir + "/" + e.Name()
		found[path] = gitx.RemoteURL(dir)
		paths = append(paths, path)
	}
	sort.Strings(paths)

	byPath := map[string]*ManifestEntry{}
	for i := range m.Projects {
		byPath[m.Projects[i].Path] = &m.Projects[i]
	}
	var add []ManifestEntry
	moved := map[int]bool{} // missing entries whose id a moved repo took
	for _, path := range paths {
		remote := found[path]
		if e := byPath[path]; e != nil {
			if e.Missing {
				e.Missing = false
				res.Returned = append(res.Returned, path)
			}
			if remote != e.Remote {
				e.Remote = remote
				res.Updated = append(res.Updated, path)
			}
			if remote != "" && lookup != nil {
				if id, ok := lookup(remote); ok && id != e.ID && m.ByID(id) == nil {
					res.Adopted = append(res.Adopted, path+": "+e.ID+" -> "+id)
					e.ID = id
				}
			}
			continue
		}
		e := ManifestEntry{Slug: Slugify(filepath.Base(path)), Path: path, Remote: remote}
		if remote != "" && lookup != nil {
			if id, ok := lookup(remote); ok && m.ByID(id) == nil && !addedID(add, id) {
				e.ID = id
			}
		}
		if e.ID == "" && remote != "" {
			// An entry whose repo is gone, with the same remote: the repo moved.
			for i := range m.Projects {
				old := &m.Projects[i]
				if _, here := found[old.Path]; !here && !moved[i] && gitx.NormalizeRemote(old.Remote) == gitx.NormalizeRemote(remote) {
					e.ID = old.ID
					moved[i] = true
					break
				}
			}
		}
		if e.ID == "" {
			e.ID = NewID()
		}
		add = append(add, e)
		res.Added = append(res.Added, path)
	}
	kept := m.Projects[:0:0]
	for i := range m.Projects {
		e := m.Projects[i]
		if moved[i] {
			continue
		}
		if _, ok := found[e.Path]; !ok && !e.Missing {
			e.Missing = true
			res.Missing = append(res.Missing, e.Path)
		}
		kept = append(kept, e)
	}
	m.Projects = append(kept, add...)
	if res.Changed() || !p.HasManifest() {
		if err := m.Save(p); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func addedID(add []ManifestEntry, id string) bool {
	for _, e := range add {
		if e.ID == id {
			return true
		}
	}
	return false
}
