// Package project is the binding between a directory and a Gears project:
// the committed .gears/project.yaml, and the paths that hang off it.
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
	LocalFile    = "local.db"
	// legacyCacheFile is the pre-rename name; Open moves it once.
	legacyCacheFile = "cache.db"
	RunPidFile      = "run.pid"
)

// Project is the binding file plus the root it was found in.
type Project struct {
	// ID is minted once at init (UUID v7, so it sorts by time) and committed,
	// so every clone agrees on which project this is. It is the key the
	// server will know the project by; slugs are for humans and collide.
	ID      string    `yaml:"id"`
	Slug    string    `yaml:"slug"`
	Created time.Time `yaml:"created"`
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

func (p *Project) GearsDir() string        { return filepath.Join(p.Root, Dir) }
func (p *Project) ArtifactsDir() string    { return filepath.Join(p.GearsDir(), ArtifactsDir) }
func (p *Project) LocalPath() string       { return filepath.Join(p.GearsDir(), LocalFile) }
func (p *Project) LegacyLocalPath() string { return filepath.Join(p.GearsDir(), legacyCacheFile) }

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
			// Bindings written before ids existed get one now, once.
			if p.ID == "" {
				p.ID = NewID()
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

// Init binds dir to a project. It is idempotent: anything already present is
// kept and reported as such.
func Init(dir, slug string) (*InitResult, error) {
	if slug == "" {
		slug = Slugify(filepath.Base(dir))
	}
	if slug == "" {
		return nil, errors.New("could not derive a slug; pass --slug")
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
		res.Actions = append(res.Actions, "kept    "+rel(dir, bindingPath)+" (slug "+p.Slug+")")
	} else {
		p = Project{ID: NewID(), Slug: slug, Created: time.Now().UTC().Truncate(time.Second)}
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

var ignoreLines = []string{
	Dir + "/" + LocalFile,
	Dir + "/" + RunPidFile,
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
