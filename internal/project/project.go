// Package project is the binding between a directory and a Gears project:
// .gears/project.yaml (never committed) and the paths that hang off it.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const (
	Dir          = ".gears"
	BindingFile  = "project.yaml"
	ArtifactsDir = "artifacts"
	// GearboxDir holds machine state under .gears/: the local record, the
	// run pid, and later sync's cursor and change log. One directory, one
	// gitignore line; a dot-directory so it reads like .git/.
	GearboxDir = ".gearbox"
	LocalFile  = "local.db"
	RunPidFile = "run.pid"
	// legacyCacheFile is the pre-rename name; Open moves it once.
	legacyCacheFile = "cache.db"
)

// Project is the binding file plus the root it was found in.
type Project struct {
	// ID is minted once at init (UUID v7, so it sorts by time) and committed,
	// so every clone agrees on which project this is. It is the key the
	// server will know the project by; slugs are for humans and collide.
	ID   string `yaml:"id"`
	Slug string `yaml:"slug"`
	// Prefix is the card key prefix (GRS in GRS-14): two to eight A-Z0-9,
	// derived from the slug unless init --prefix says otherwise. Committed, so
	// every clone mints the same keys.
	Prefix  string    `yaml:"prefix"`
	Created time.Time `yaml:"created"`
	// Workspace is the UUID of the workspace this project belongs to. Empty
	// means: the nearest ancestor binding is the workspace, or, with none,
	// this binding is itself the workspace. Written by gears login.
	Workspace string `yaml:"workspace,omitempty"`
	// Root is the directory that holds .gears/. Not serialised.
	Root string `yaml:"-"`
}

// NewID mints a UUID v7. Every row Gears creates uses this, so ids made
// offline are globally unique and time-ordered when they reach the server.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func (p *Project) GearsDir() string     { return filepath.Join(p.Root, Dir) }
func (p *Project) ArtifactsDir() string { return filepath.Join(p.GearsDir(), ArtifactsDir) }
func (p *Project) GearboxPath() string  { return filepath.Join(p.GearsDir(), GearboxDir) }
func (p *Project) LocalPath() string    { return filepath.Join(p.GearboxPath(), LocalFile) }
func (p *Project) RunPidPath() string   { return filepath.Join(p.GearboxPath(), RunPidFile) }

// LegacyLocalPaths are the places the record lived before .gearbox/, newest
// first. Open moves the first one it finds, once.
func (p *Project) LegacyLocalPaths() []string {
	return []string{filepath.Join(p.GearsDir(), LocalFile), filepath.Join(p.GearsDir(), legacyCacheFile)}
}

// WorkspaceID resolves the workspace this project syncs to: the explicit
// field, else the nearest ancestor binding's id, else this project's own id
// (a root binding is its own workspace).
func (p *Project) WorkspaceID() string {
	if p.Workspace != "" {
		return p.Workspace
	}
	if anc := p.Ancestor(); anc != nil {
		return anc.WorkspaceID()
	}
	return p.ID
}

// IsWorkspace reports whether this binding is the workspace itself.
func (p *Project) IsWorkspace() bool { return p.WorkspaceID() == p.ID }

// Ancestor returns the nearest binding above this one, or nil.
func (p *Project) Ancestor() *Project {
	parent := filepath.Dir(p.Root)
	if parent == p.Root {
		return nil
	}
	anc, err := Find(parent)
	if err != nil {
		return nil
	}
	return anc
}

// Save rewrites the binding file.
func (p *Project) Save() error {
	out, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.GearsDir(), BindingFile), out, 0o644)
}

// ErrNotBound is returned when no .gears/project.yaml is found walking up.
var ErrNotBound = errors.New("no Gears project here; run: gears init")

// Find walks up from start looking for .gears/project.yaml.
func Find(start string) (*Project, error) {
	dir := start
	for {
		path := filepath.Join(dir, Dir, BindingFile)
		if data, err := os.ReadFile(path); err == nil {
			var p Project
			if err := yaml.Unmarshal(data, &p); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			p.Root = dir
			// Bindings written before ids or prefixes existed get them now, once.
			changed := false
			if p.ID == "" {
				p.ID, changed = NewID(), true
			}
			if p.Prefix == "" {
				p.Prefix, changed = DerivePrefix(p.Slug), true
			}
			if changed {
				if out, err := yaml.Marshal(&p); err == nil {
					_ = os.WriteFile(path, out, 0o644)
				}
			}
			return &p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNotBound
		}
		dir = parent
	}
}

// InitResult reports what Init did, one line per action, for the CLI to print.
type InitResult struct {
	Project *Project
	Actions []string
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify lowercases and reduces anything that is not a-z0-9 to single dashes.
func Slugify(s string) string {
	s = slugRe.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(s, "-")
}

// DerivePrefix takes the first four A-Z0-9 characters of a slug, upper-cased:
// gears -> GEAR, churchvida -> CHUR. byz-kan does the same for board names.
func DerivePrefix(slug string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(slug) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 4 {
			break
		}
	}
	if b.Len() < 2 {
		return "CARD"
	}
	return b.String()
}

// NormalizePrefix validates a user-supplied prefix: A-Z0-9 only, 2 to 8 long.
func NormalizePrefix(raw string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			return "", fmt.Errorf("prefix %q: letters and digits only", raw)
		}
	}
	if b.Len() < 2 || b.Len() > 8 {
		return "", fmt.Errorf("prefix %q: 2 to 8 characters", raw)
	}
	return b.String(), nil
}

// Init binds dir to a project. It is idempotent: anything already present is
// kept and reported as such.
func Init(dir, slug, prefix string) (*InitResult, error) {
	if slug == "" {
		slug = Slugify(filepath.Base(dir))
	}
	if slug == "" {
		return nil, errors.New("could not derive a slug; pass --slug")
	}
	if prefix != "" {
		var err error
		if prefix, err = NormalizePrefix(prefix); err != nil {
			return nil, err
		}
	} else {
		prefix = DerivePrefix(slug)
	}
	res := &InitResult{}
	gears := filepath.Join(dir, Dir)
	bindingPath := filepath.Join(gears, BindingFile)

	// Binding.
	var p Project
	if data, err := os.ReadFile(bindingPath); err == nil {
		if err := yaml.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("%s: %w", bindingPath, err)
		}
		if p.Prefix == "" {
			p.Prefix = prefix
			if out, err := yaml.Marshal(&p); err == nil {
				_ = os.WriteFile(bindingPath, out, 0o644)
			}
		}
		res.Actions = append(res.Actions, "kept    "+rel(dir, bindingPath)+" (slug "+p.Slug+", prefix "+p.Prefix+")")
	} else {
		p = Project{ID: NewID(), Slug: slug, Prefix: prefix, Created: time.Now().UTC().Truncate(time.Second)}
		if err := os.MkdirAll(gears, 0o755); err != nil {
			return nil, err
		}
		out, err := yaml.Marshal(&p)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(bindingPath, out, 0o644); err != nil {
			return nil, err
		}
		res.Actions = append(res.Actions, "wrote   "+rel(dir, bindingPath))
	}
	p.Root = dir
	res.Project = &p

	// Artifacts directory.
	if err := ensureDir(p.ArtifactsDir(), dir, res); err != nil {
		return nil, err
	}

	// Gitignore: the cache and the run pid are local only.
	if err := ensureGitignore(dir, res); err != nil {
		return nil, err
	}

	// AGENTS.md block.
	if err := ensureAgentsBlock(dir, res); err != nil {
		return nil, err
	}
	return res, nil
}

func ensureDir(path, root string, res *InitResult) error {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		res.Actions = append(res.Actions, "kept    "+rel(root, path)+"/")
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	res.Actions = append(res.Actions, "created "+rel(root, path)+"/")
	return nil
}

// .gears/ is never committed (decided 2026-10-12): the persistent store is
// gearsos.com, the directory is binding plus cache.
var ignoreLines = []string{
	Dir + "/",
}

func ensureGitignore(root string, res *InitResult) error {
	path := filepath.Join(root, ".gitignore")
	existing, _ := os.ReadFile(path)
	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, l := range ignoreLines {
		if !have[l] {
			add = append(add, l)
		}
	}
	if len(add) == 0 {
		res.Actions = append(res.Actions, "kept    .gitignore")
		return nil
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	if len(existing) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("# Gears local state\n")
	for _, l := range add {
		b.WriteString(l + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	res.Actions = append(res.Actions, "updated .gitignore ("+strings.Join(add, ", ")+")")
	return nil
}

const (
	agentsStart = "<!-- gears:start -->"
	agentsEnd   = "<!-- gears:end -->"
)

// agentsBlock is deliberately short. The record is the source of truth;
// this file only tells an agent how to reach it.
const agentsBlock = agentsStart + `
## Gears

This directory is bound to a Gears project. Start every session with:

    gears status

Then read the latest session entries it shows before doing anything else.
Decisions go in .gears/artifacts/ as adr--*.md (gears artifact new adr "...").
Log work as it happens: gears session new --kind done|decision|problem|next "...".
` + agentsEnd + "\n"

func ensureAgentsBlock(root string, res *InitResult) error {
	path := filepath.Join(root, "AGENTS.md")
	existing, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(existing), agentsStart) {
		res.Actions = append(res.Actions, "kept    AGENTS.md (gears block present)")
		return nil
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	if len(existing) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(agentsBlock)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	verb := "wrote   "
	if len(existing) > 0 {
		verb = "appended"
	}
	res.Actions = append(res.Actions, verb+" AGENTS.md (gears block)")
	return nil
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}
