// Package migrate upgrades a gears v2 workspace to v3 (GRS-32).
//
// The v2 CLI (github.com/Syluxso/gears) kept everything under .gears/:
//
//	.gearbox/config.json   workspace id, API URL, token     (ignored)
//	.gearbox/gears.db      SQLite content index              (ignored)
//	.gearbox/hydrate.md    hydrate instructions              (ignored)
//	artifacts/adr--*.md    ADRs (adr_*.md in older trees, adr_template.md and
//	                       adr_example-*.md shipped by v2 init)
//	artifacts/index.md
//	decisions/*.md         decision log (index.md)
//	story/story--*.md      stories (index.md)
//	sessions/*.md          YYYY-MM-DD.md logs, hydrate-report-*.md
//	context/ memory/ instructions/  index.md (+ memory/projects/<slug>.md)
//	index.md README.md gears-init.md
//
// Migration renames .gears to .gears_v2, zips that tree to .gears_v2.zip,
// binds a v3 workspace in the same directory and copies each markdown file
// into .gears/artifacts/ as free--migrated--<kind>-<name>.md with a title in
// its front matter (plus date and migrated_from). All free: no shape checks. The old database and
// v2-only machinery are left behind in .gears_v2. Re-running is safe: names
// already present are kept.
package migrate

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/shape"
)

const (
	OldDir  = ".gears_v2"
	ZipFile = ".gears_v2.zip"
	// Marker sits between the prefix and the name of every migrated file.
	Marker = "migrated"
)

// mdPrefixes are the v3 markdown prefixes a v2 file name can carry.
var mdPrefixes = []string{"adr", "story", "idea", "index", "free"}

// Item is one v2 file and what migration does with it.
type Item struct {
	Source string `json:"source"` // slash path inside the v2 tree
	Prefix string `json:"prefix,omitempty"`
	File   string `json:"file,omitempty"` // v3 artifact file name
	Title  string `json:"title,omitempty"`
	// Action is create, exists (already migrated, kept) or skip.
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// Plan is everything Run would do, computed without touching the disk.
type Plan struct {
	Dir    string `json:"dir"`
	Rename bool   `json:"rename"` // .gears -> .gears_v2
	Zip    bool   `json:"zip"`    // write .gears_v2.zip
	Init   bool   `json:"init"`   // no v3 binding yet
	Items  []Item `json:"items"`
	// src is where the v2 files are right now (.gears before the rename).
	src string
}

// Counts tallies the plan's items by action.
func (p *Plan) Counts() (create, exists, skip int) {
	for _, it := range p.Items {
		switch it.Action {
		case "create":
			create++
		case "exists":
			exists++
		default:
			skip++
		}
	}
	return
}

// Result is what Run did.
type Result struct {
	Plan    *Plan
	Project *project.Project
	Init    []string // init's action lines
}

// Detect inspects dir and returns the plan, or refuses: inside a v3
// workspace, already v3 with nothing to migrate, no v2 tree, or both a v2
// .gears and a .gears_v2.
func Detect(dir string) (*Plan, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if parent := filepath.Dir(dir); parent != dir {
		if anc, err := project.Find(parent); err == nil {
			return nil, fmt.Errorf("%s is inside the Gears v3 workspace at %s; migrate runs only at a workspace root", dir, anc.Root)
		}
	}
	gears := filepath.Join(dir, project.Dir)
	old := filepath.Join(dir, OldDir)
	hasGears, hasOld := isDir(gears), isDir(old)
	isV3 := isFile(filepath.Join(gears, project.BindingFile))
	plan := &Plan{Dir: dir}
	switch {
	case hasGears && !isV3:
		if hasOld {
			return nil, fmt.Errorf("%s has both a v2 .gears and %s; move one aside and run again", dir, OldDir)
		}
		if !looksV2(gears) {
			return nil, fmt.Errorf("%s does not look like a gears v2 tree (no .gearbox/config.json, index.md, artifacts/, story/, decisions/ or sessions/)", gears)
		}
		if isFile(filepath.Join(dir, ZipFile)) {
			return nil, fmt.Errorf("%s already exists; move it aside so migrate can write a fresh archive", filepath.Join(dir, ZipFile))
		}
		plan.Rename, plan.Zip, plan.Init, plan.src = true, true, true, gears
	case hasOld:
		// A previous run got at least as far as the rename: resume.
		plan.Zip = !isFile(filepath.Join(dir, ZipFile))
		plan.Init = !isV3
		plan.src = old
	case isV3:
		return nil, fmt.Errorf("%s is already a Gears v3 workspace and has no %s; nothing to migrate", dir, OldDir)
	default:
		return nil, fmt.Errorf("no gears v2 workspace at %s (no .gears directory)", dir)
	}
	items, err := classifyTree(plan.src)
	if err != nil {
		return nil, err
	}
	artDir := filepath.Join(gears, project.ArtifactsDir)
	for i := range items {
		if items[i].Action == "create" && isFile(filepath.Join(artDir, items[i].File)) && isV3 {
			items[i].Action = "exists"
		}
	}
	plan.Items = items
	return plan, nil
}

// Run carries out the plan: rename, zip, init, then write each artifact
// and reconcile so the record adopts them.
func Run(plan *Plan) (*Result, error) {
	dir := plan.Dir
	res := &Result{Plan: plan}
	old := filepath.Join(dir, OldDir)
	if plan.Rename {
		if err := os.Rename(filepath.Join(dir, project.Dir), old); err != nil {
			return nil, fmt.Errorf("rename .gears to %s (is the old gears still running?): %w", OldDir, err)
		}
	}
	if plan.Zip {
		if err := writeZip(old, filepath.Join(dir, ZipFile)); err != nil {
			return nil, err
		}
	}
	ir, err := project.Init(dir, "", "")
	if err != nil {
		return nil, err
	}
	res.Project, res.Init = ir.Project, ir.Actions
	artDir := ir.Project.ArtifactsDir()
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Action != "create" {
			continue
		}
		dst := filepath.Join(artDir, it.File)
		if isFile(dst) {
			it.Action = "exists"
			continue
		}
		src := filepath.Join(old, filepath.FromSlash(it.Source))
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		front := [][2]string{{"title", it.Title}}
		if st, err := os.Stat(src); err == nil {
			front = append(front, [2]string{"date", st.ModTime().Format("2006-01-02")})
		}
		front = append(front, [2]string{"migrated_from", OldDir + "/" + it.Source})
		if err := os.WriteFile(dst, []byte(withFront(string(data), front)), 0o644); err != nil {
			return nil, err
		}
	}
	if _, err := artifact.Reconcile(ir.Project); err != nil {
		return nil, fmt.Errorf("artifacts written; reconcile: %w (run gears artifact reconcile)", err)
	}
	return res, nil
}

// classifyTree walks a v2 tree in a stable order and maps each file. Names
// that collide get -2, -3 in walk order, so a re-run maps the same way.
func classifyTree(root string) ([]Item, error) {
	var rels []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".gearbox" {
				return filepath.SkipDir
			}
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(rels)
	items := []Item{{Source: ".gearbox/", Action: "skip", Reason: "v2 config, database and hydrate file; not migrated"}}
	used := map[string]bool{}
	for _, rel := range rels {
		it := classify(rel)
		if it.Action == "create" {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return nil, err
			}
			base := strings.TrimSuffix(it.File, ".md")
			file := it.File
			for n := 2; used[file]; n++ {
				file = fmt.Sprintf("%s-%d.md", base, n)
			}
			used[file] = true
			it.File = file
			it.Title = titleOf(string(data), it.Title)
		}
		items = append(items, it)
	}
	return items, nil
}

// classify maps one v2 path (slash form, relative to the tree) to its v3
// prefix and file name, or a skip with the reason.
func classify(rel string) Item {
	it := Item{Source: rel}
	dirPart, base := "", rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		dirPart, base = rel[:i], rel[i+1:]
	}
	top := strings.SplitN(rel, "/", 2)[0]
	if dirPart == "" {
		top = ""
	}
	skip := func(reason string) Item { it.Action, it.Reason = "skip", reason; return it }
	if !strings.EqualFold(filepath.Ext(base), ".md") {
		return skip("not markdown; kept in " + OldDir)
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	switch {
	case dirPart == "" && (base == "gears-init.md" || base == "README.md"):
		return skip("v2 framework instructions")
	case top == "artifacts" && (base == "adr_template.md" || strings.HasPrefix(base, "adr_example-")):
		return skip("v2 template or shipped example")
	case top == "sessions" && strings.HasPrefix(base, "hydrate-report-"):
		return skip("v2 hydrate report")
	}

	prefix, name := "", stem
	if strings.EqualFold(base, "index.md") {
		prefix, name = "index", strings.ReplaceAll(dirPart, "/", "-")
		if name == "" {
			name = "workspace"
		}
	} else {
		for _, p := range mdPrefixes {
			if r, ok := cut(stem, p+"--"); ok {
				prefix, name = p, r
				break
			}
			if r, ok := cut(stem, p+"_"); ok {
				prefix, name = p, r
				break
			}
		}
		if prefix == "" {
			switch top {
			case "decisions":
				prefix = "adr"
			case "story":
				prefix = "story"
			default:
				prefix = "free"
			}
		}
		// Files outside the typed folders keep their folder in the name:
		// sessions/2026-04-01.md -> free--migrated--sessions-2026-04-01.md.
		if top != "" && top != "artifacts" && top != "decisions" && top != "story" {
			name = strings.ReplaceAll(dirPart, "/", "-") + "-" + name
		}
	}
	// Everything lands as free (Darryn: no shape checks on migrated work);
	// the v2 kind stays visible as the start of the name.
	if prefix != "free" {
		name = prefix + "-" + name
	}
	slug := project.Slugify(name)
	if slug == "" {
		slug = "untitled"
	}
	it.Action, it.Prefix = "create", "free"
	it.File = "free--" + Marker + "--" + slug + ".md"
	it.Title = strings.ReplaceAll(strings.ReplaceAll(name, "-", " "), "_", " ")
	return it
}

func cut(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) && len(s) > len(prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// titleOf prefers a title already in front matter, then the first "# "
// heading, then the fallback derived from the name.
func titleOf(body, fallback string) string {
	doc, err := shape.Split(body)
	if err == nil {
		if t, ok := doc.FrontMatter["title"].(string); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	} else {
		doc.Body = body
	}
	for _, line := range strings.Split(strings.ReplaceAll(doc.Body, "\r\n", "\n"), "\n") {
		if t, ok := cut(strings.TrimSpace(line), "# "); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	return fallback
}

// withFront keeps the body and adds each front matter key it lacks (title,
// status and date for the shape, migrated_from for provenance). Existing
// front matter is kept line for line.
func withFront(body string, keys [][2]string) string {
	doc, err := shape.Split(body)
	has := err == nil && strings.HasPrefix(strings.ReplaceAll(body, "\r\n", "\n"), "---\n")
	add := ""
	for _, kv := range keys {
		if _, ok := doc.FrontMatter[kv[0]]; has && ok {
			continue
		}
		add += fmLine(kv[0], kv[1])
	}
	if has {
		i := strings.Index(body, "\n") + 1
		return body[:i] + add + body[i:]
	}
	return "---\n" + add + "---\n\n" + body
}

func fmLine(k, v string) string {
	out, err := yaml.Marshal(map[string]string{k: v})
	if err != nil {
		return k + ": \"\"\n"
	}
	return string(out)
}

// writeZip archives the tree at src into dst, entries under .gears_v2/.
func writeZip(src, dst string) error {
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	werr := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		name := OldDir + "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." {
				name = OldDir
			}
			_, err := zw.Create(name + "/")
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Method = name, zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
	cerr := zw.Close()
	ferr := f.Close()
	if err := errors.Join(werr, cerr, ferr); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", ZipFile, err)
	}
	return os.Rename(tmp, dst)
}

func looksV2(gears string) bool {
	for _, m := range []string{".gearbox/config.json", "index.md", "gears-init.md", "artifacts", "story", "decisions", "sessions", "memory", "context"} {
		if _, err := os.Stat(filepath.Join(gears, filepath.FromSlash(m))); err == nil {
			return true
		}
	}
	return false
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
