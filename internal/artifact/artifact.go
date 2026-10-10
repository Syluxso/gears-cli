// Package artifact manages the prefixed files in .gears/artifacts/ and the
// rows that rule them. Row first, file second: New inserts the record, then
// writes the file. The file is the data (body, and the title/status an agent
// may edit in front matter); the row is the rule (identity, tags, hash,
// shape result). Reconcile keeps the two honest.
package artifact

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"


	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/shape"
	"github.com/Syluxso/gears-cli/internal/store"
)

// Prefixes in display order. "shape" files live beside these but are schemas,
// not artifacts, so they are not listed.
var Prefixes = []string{"adr", "story", "idea", "index", "free", "runbook"}

// Ext is the file extension per prefix. Runbooks are YAML definitions; the
// rest are markdown with front matter.
func Ext(prefix string) string {
	if prefix == "runbook" {
		return ".yaml"
	}
	return ".md"
}

// Artifact is a row joined with what is on disk.
type Artifact struct {
	ID          string
	Prefix      string
	Slug        string
	File        string // base name
	Path        string // absolute
	Title       string
	Status      string
	CreatedAt   time.Time
	CreatedBy   string
	UpdatedAt   time.Time
	BodyHash    string
	FilePresent bool
	ShapeOK     bool
	ShapeErrors []string
	Tags        []string
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

// Parse splits "<prefix>--<slug>.<ext>" into its parts; ok is false for
// files that are not artifacts (shapes, conflicts, stray files).
func Parse(file string) (prefix, slug string, ok bool) {
	i := strings.Index(file, sep)
	if i <= 0 {
		return "", "", false
	}
	prefix = file[:i]
	if !isPrefix(prefix) {
		return "", "", false
	}
	rest := file[i+len(sep):]
	if !strings.HasSuffix(rest, Ext(prefix)) {
		return "", "", false
	}
	slug = strings.TrimSuffix(rest, Ext(prefix))
	if slug == "" {
		return "", "", false
	}
	return prefix, slug, true
}

// New creates the row, then the file seeded from the prefix's shape. ADRs
// are numbered (adr--001-...) so their order is visible in a listing.
func New(p *project.Project, prefix, title, agent string) (*Artifact, error) {
	if !isPrefix(prefix) {
		return nil, fmt.Errorf("unknown prefix %q; one of %s", prefix, strings.Join(Prefixes, ", "))
	}
	if prefix == "runbook" {
		return nil, fmt.Errorf("runbooks are authored with: gears runbook new %q", title)
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
	if agent == "" {
		agent = session.DefaultAgent()
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
	file := prefix + sep + slug + Ext(prefix)
	path := filepath.Join(p.ArtifactsDir(), file)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s already exists", file)
	}

	front := map[string]string{
		"title":  title,
		"date":   time.Now().Format("2006-01-02"),
		"status": "draft",
	}
	wanted := map[string]string{}
	for _, k := range s.FrontMatter {
		wanted[k] = front[k]
	}
	body := s.Seed(wanted)
	status := ""
	if containsKey(s.FrontMatter, "status") {
		status = "draft"
	}

	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Row first. The id is minted here so the artifact exists before any
	// server has seen it.
	a := &Artifact{
		ID: project.NewID(), Prefix: prefix, Slug: slug, File: file, Path: path,
		Title: title, Status: status, CreatedBy: agent,
		BodyHash: hash(body), FilePresent: true,
	}
	now := store.Now()
	errs := s.Validate(mustSplit(body))
	a.ShapeOK = len(errs) == 0
	a.ShapeErrors = errs
	if _, err := db.Exec(`INSERT INTO artifacts
		(id, prefix, slug, file, title, status, created_at, created_by, updated_at, body_hash, file_present, shape_ok, shape_errors)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		a.ID, a.Prefix, a.Slug, a.File, a.Title, a.Status, now, a.CreatedBy, now, a.BodyHash, b2i(a.ShapeOK), strings.Join(errs, "; "),
	); err != nil {
		return nil, err
	}
	a.CreatedAt, a.UpdatedAt = store.Parse(now), store.Parse(now)

	// File second.
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		_, _ = db.Exec(`UPDATE artifacts SET file_present = 0 WHERE id = ?`, a.ID)
		return nil, err
	}
	return a, nil
}

// List returns every artifact row, with tags, in prefix order then name.
// It reconciles first so the listing reflects the disk.
func List(p *project.Project) ([]Artifact, error) {
	if _, err := Reconcile(p); err != nil {
		return nil, err
	}
	if !store.Exists(p) {
		return nil, nil
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return list(db, p)
}

func list(db *sql.DB, p *project.Project) ([]Artifact, error) {
	rows, err := db.Query(`SELECT id, prefix, slug, file, title, status, created_at, created_by, updated_at, body_hash, file_present, shape_ok, shape_errors FROM artifacts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var created, updated, serrs string
		var present, ok int
		if err := rows.Scan(&a.ID, &a.Prefix, &a.Slug, &a.File, &a.Title, &a.Status, &created, &a.CreatedBy, &updated, &a.BodyHash, &present, &ok, &serrs); err != nil {
			return nil, err
		}
		a.CreatedAt, a.UpdatedAt = store.Parse(created), store.Parse(updated)
		a.FilePresent, a.ShapeOK = present == 1, ok == 1
		if serrs != "" {
			a.ShapeErrors = strings.Split(serrs, "; ")
		}
		a.Path = filepath.Join(p.ArtifactsDir(), a.File)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tags, err := db.Query(`SELECT artifact_id, tag FROM artifact_tags ORDER BY tag`)
	if err != nil {
		return nil, err
	}
	defer tags.Close()
	byID := map[string][]string{}
	for tags.Next() {
		var id, tag string
		if err := tags.Scan(&id, &tag); err != nil {
			return nil, err
		}
		byID[id] = append(byID[id], tag)
	}
	for i := range out {
		out[i].Tags = byID[out[i].ID]
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

// Find returns one artifact by file name or id.
func Find(p *project.Project, key string) (*Artifact, error) {
	all, err := List(p)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].File == key || all[i].ID == key || strings.HasPrefix(all[i].ID, key) {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("no artifact %q", key)
}

// Tag adds (+name) and removes (-name) tags on an artifact. Bare names add.
func Tag(p *project.Project, key string, changes []string) (*Artifact, error) {
	a, err := Find(p, key)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	for _, c := range changes {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		switch {
		case strings.HasPrefix(c, "-"):
			if _, err := db.Exec(`DELETE FROM artifact_tags WHERE artifact_id = ? AND tag = ?`, a.ID, project.Slugify(c[1:])); err != nil {
				return nil, err
			}
		default:
			tag := project.Slugify(strings.TrimPrefix(c, "+"))
			if tag == "" {
				continue
			}
			if _, err := db.Exec(`INSERT OR IGNORE INTO artifact_tags (artifact_id, tag) VALUES (?, ?)`, a.ID, tag); err != nil {
				return nil, err
			}
		}
	}
	_, _ = db.Exec(`UPDATE artifacts SET updated_at = ? WHERE id = ?`, store.Now(), a.ID)
	return Find(p, a.File)
}

// Change is one thing Reconcile did, for the CLI to print.
type Change struct {
	Action string // regenerated | adopted | updated | missing
	File   string
	Detail string
}

// Reconcile walks rows and files and makes them agree:
//   - row, no file: regenerate the file from the shape seed and row metadata
//   - file, no row: adopt it (new row), validate, flag
//   - both, hash differs: file wins; update title/status/hash/shape result
//
// It is safe to run at any time and is run by list and status.
func Reconcile(p *project.Project) ([]Change, error) {
	entries, _ := os.ReadDir(p.ArtifactsDir())
	onDisk := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, _, ok := Parse(e.Name()); ok {
			onDisk[e.Name()] = true
		}
	}
	if len(onDisk) == 0 && !store.Exists(p) {
		return nil, nil
	}

	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := list(db, p)
	if err != nil {
		return nil, err
	}
	set, err := shape.Load(p.ArtifactsDir())
	if err != nil {
		return nil, err
	}

	var changes []Change
	seen := map[string]bool{}
	for _, a := range rows {
		seen[a.File] = true
		if !onDisk[a.File] {
			// Regenerate from the shape seed and what the row knows.
			s := set[a.Prefix]
			body := ""
			if s != nil && a.Prefix != "runbook" {
				front := map[string]string{"title": a.Title, "status": a.Status, "date": a.CreatedAt.Format("2006-01-02")}
				wanted := map[string]string{}
				for _, k := range s.FrontMatter {
					wanted[k] = front[k]
				}
				body = s.Seed(wanted)
			}
			if err := os.MkdirAll(p.ArtifactsDir(), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(a.Path, []byte(body), 0o644); err != nil {
				return nil, err
			}
			errs := validate(set, a.Prefix, body)
			_, _ = db.Exec(`UPDATE artifacts SET file_present = 1, body_hash = ?, shape_ok = ?, shape_errors = ?, updated_at = ? WHERE id = ?`,
				hash(body), b2i(len(errs) == 0), strings.Join(errs, "; "), store.Now(), a.ID)
			changes = append(changes, Change{"regenerated", a.File, "from row (file was missing)"})
			continue
		}
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, err
		}
		body := string(data)
		h := hash(body)
		if h == a.BodyHash && a.FilePresent {
			continue
		}
		// File wins for body, title, status.
		title, status := a.Title, a.Status
		if a.Prefix != "runbook" {
			if doc, err := shape.Split(body); err == nil {
				if t, ok := doc.FrontMatter["title"].(string); ok && strings.TrimSpace(t) != "" {
					title = t
				}
				if st, ok := doc.FrontMatter["status"].(string); ok {
					status = st
				}
			}
		}
		errs := validate(set, a.Prefix, body)
		_, _ = db.Exec(`UPDATE artifacts SET file_present = 1, body_hash = ?, title = ?, status = ?, shape_ok = ?, shape_errors = ?, updated_at = ? WHERE id = ?`,
			h, title, status, b2i(len(errs) == 0), strings.Join(errs, "; "), store.Now(), a.ID)
		changes = append(changes, Change{"updated", a.File, "file changed; row follows"})
	}

	// Files with no row: adopt.
	for file := range onDisk {
		if seen[file] {
			continue
		}
		prefix, slug, _ := Parse(file)
		path := filepath.Join(p.ArtifactsDir(), file)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		body := string(data)
		title, status := slug, ""
		if prefix != "runbook" {
			if doc, err := shape.Split(body); err == nil {
				if t, ok := doc.FrontMatter["title"].(string); ok && strings.TrimSpace(t) != "" {
					title = t
				}
				if st, ok := doc.FrontMatter["status"].(string); ok {
					status = st
				}
			}
		}
		errs := validate(set, prefix, body)
		now := store.Now()
		if _, err := db.Exec(`INSERT INTO artifacts
			(id, prefix, slug, file, title, status, created_at, created_by, updated_at, body_hash, file_present, shape_ok, shape_errors)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			project.NewID(), prefix, slug, file, title, status, now, "adopted", now, hash(body), b2i(len(errs) == 0), strings.Join(errs, "; "),
		); err != nil {
			return nil, err
		}
		changes = append(changes, Change{"adopted", file, "file had no row"})
	}
	return changes, nil
}

// Validate re-reads one artifact and checks it against its shape. Runbooks
// are validated by the runbook package; here they only need to exist.
func Validate(p *project.Project, a Artifact) []string {
	set, err := shape.Load(p.ArtifactsDir())
	if err != nil {
		return []string{err.Error()}
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return []string{"file missing: " + a.File}
	}
	return validate(set, a.Prefix, string(data))
}

func validate(set shape.Set, prefix, body string) []string {
	if prefix == "runbook" {
		if strings.TrimSpace(body) == "" {
			return []string{"empty runbook"}
		}
		return nil
	}
	s, ok := set[prefix]
	if !ok {
		return []string{"no shape for prefix " + prefix}
	}
	doc, err := shape.Split(body)
	if err != nil {
		return []string{err.Error()}
	}
	return s.Validate(doc)
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

func hash(s string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(s, "\r\n", "\n")))
	return hex.EncodeToString(sum[:8])
}

func mustSplit(body string) shape.Document {
	doc, _ := shape.Split(body)
	return doc
}

func containsKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
