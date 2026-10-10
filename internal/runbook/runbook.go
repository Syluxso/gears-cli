// Package runbook holds the two halves of a process an agent cannot fake:
// the definition (a runbook--*.yaml artifact, authored only here, with a
// fixed step vocabulary) and the run (a record of one execution, with a
// timestamp and verified evidence per step).
package runbook

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/Syluxso/gears-cli/internal/desk"
	"github.com/Syluxso/gears-cli/internal/gitx"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
)

// Kinds is the whole step vocabulary.
var Kinds = []string{"ask", "do", "gears", "git", "gate"}

// Checks a step may declare, by kind. The check names what the evidence must
// be and how it is verified. A kind with no check accepts any non-empty
// evidence and is marked done, not verified.
var Checks = map[string][]string{
	"ask":   {},
	"do":    {"commit"},
	"gears": {"session", "desk", "artifact"},
	"git":   {"branch", "commit", "pushed"},
	"gate":  {},
}

// Step is one line of a runbook definition.
type Step struct {
	Kind  string `yaml:"kind"`
	Text  string `yaml:"text"`
	Check string `yaml:"check,omitempty"`
}

// Runbook is the definition file.
type Runbook struct {
	Title   string `yaml:"title"`
	Slug    string `yaml:"slug"`
	Created string `yaml:"created"`
	Steps   []Step `yaml:"steps"`
	// Path is where it was loaded from. Not serialised.
	Path string `yaml:"-"`
}

func path(p *project.Project, slug string) string {
	return filepath.Join(p.ArtifactsDir(), "runbook--"+slug+".yaml")
}

// New writes an empty runbook definition.
func New(p *project.Project, title string) (*Runbook, error) {
	slug := project.Slugify(title)
	if slug == "" {
		return nil, fmt.Errorf("title %q produces an empty slug", title)
	}
	rb := &Runbook{Title: title, Slug: slug, Created: time.Now().Format("2006-01-02"), Path: path(p, slug)}
	if _, err := os.Stat(rb.Path); err == nil {
		return nil, fmt.Errorf("runbook--%s.yaml already exists", slug)
	}
	if err := os.MkdirAll(p.ArtifactsDir(), 0o755); err != nil {
		return nil, err
	}
	return rb, rb.save()
}

// Load reads a runbook by slug.
func Load(p *project.Project, slug string) (*Runbook, error) {
	data, err := os.ReadFile(path(p, slug))
	if err != nil {
		return nil, fmt.Errorf("no runbook %q (see: gears runbook list)", slug)
	}
	var rb Runbook
	if err := yaml.Unmarshal(data, &rb); err != nil {
		return nil, fmt.Errorf("runbook--%s.yaml: %w", slug, err)
	}
	rb.Path = path(p, slug)
	if rb.Slug == "" {
		rb.Slug = slug
	}
	return &rb, nil
}

// All lists the runbooks in the project.
func All(p *project.Project) ([]*Runbook, error) {
	matches, _ := filepath.Glob(filepath.Join(p.ArtifactsDir(), "runbook--*.yaml"))
	var out []*Runbook
	for _, m := range matches {
		slug := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "runbook--"), ".yaml")
		rb, err := Load(p, slug)
		if err != nil {
			return nil, err
		}
		out = append(out, rb)
	}
	return out, nil
}

// AddStep appends a step after validating kind and check.
func (rb *Runbook) AddStep(kind, text, check string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !contains(Kinds, kind) {
		return fmt.Errorf("kind %q; one of %s", kind, strings.Join(Kinds, ", "))
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("step text is empty")
	}
	check = strings.ToLower(strings.TrimSpace(check))
	if check != "" && !contains(Checks[kind], check) {
		allowed := Checks[kind]
		if len(allowed) == 0 {
			return fmt.Errorf("kind %s takes no check", kind)
		}
		return fmt.Errorf("check %q for kind %s; one of %s", check, kind, strings.Join(allowed, ", "))
	}
	rb.Steps = append(rb.Steps, Step{Kind: kind, Text: text, Check: check})
	return rb.save()
}

func (rb *Runbook) save() error {
	out, err := yaml.Marshal(rb)
	if err != nil {
		return err
	}
	header := "# Runbook. Authored with gears runbook; edit through it so the shape stays finite.\n"
	return os.WriteFile(rb.Path, append([]byte(header), out...), 0o644)
}

// Run is one execution of a runbook.
type Run struct {
	ID        string
	Runbook   string
	Target    string
	Agent     string
	StartedAt time.Time
	EndedAt   time.Time
	Status    string // active | completed | abandoned
	Steps     []RunStep
}

// RunStep is a step copied into a run, with its outcome.
type RunStep struct {
	Idx      int
	Kind     string
	Text     string
	Check    string
	State    string // pending | current | done | verified | skipped
	Evidence string
	DoneAt   time.Time
	Reason   string
	DeskItem string
}

func (r *Run) Short() string { return r.ID[:8] }

// Current returns the step in progress, or nil when the run is over.
func (r *Run) Current() *RunStep {
	for i := range r.Steps {
		if r.Steps[i].State == "current" {
			return &r.Steps[i]
		}
	}
	return nil
}

// Start creates a run from a runbook, copies its steps, and places the first
// step on the agent's desk as a work item.
func Start(p *project.Project, slug, target, agent string) (*Run, error) {
	rb, err := Load(p, slug)
	if err != nil {
		return nil, err
	}
	if len(rb.Steps) == 0 {
		return nil, fmt.Errorf("runbook %q has no steps; add some: gears runbook step add %s --kind ... \"...\"", slug, slug)
	}
	if agent == "" {
		agent = session.DefaultAgent()
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	r := &Run{ID: uuid.NewString(), Runbook: slug, Target: target, Agent: agent, Status: "active"}
	now := store.Now()
	if _, err := db.Exec(`INSERT INTO runs (id, runbook, target, agent, started_at, status) VALUES (?, ?, ?, ?, ?, 'active')`,
		r.ID, r.Runbook, r.Target, r.Agent, now); err != nil {
		return nil, err
	}
	r.StartedAt = store.Parse(now)
	for i, s := range rb.Steps {
		state := "pending"
		if i == 0 {
			state = "current"
		}
		if _, err := db.Exec(`INSERT INTO run_steps (run_id, idx, kind, text, "check", state) VALUES (?, ?, ?, ?, ?, ?)`,
			r.ID, i+1, s.Kind, s.Text, s.Check, state); err != nil {
			return nil, err
		}
	}
	db.Close()
	if err := placeOnDesk(p, r.ID, 1, agent); err != nil {
		return nil, err
	}
	return Get(p, r.ID)
}

// placeOnDesk puts step idx on the agent's desk as a work item and records
// the item id on the step.
func placeOnDesk(p *project.Project, runID string, idx int, agent string) error {
	r, err := Get(p, runID)
	if err != nil {
		return err
	}
	var st *RunStep
	for i := range r.Steps {
		if r.Steps[i].Idx == idx {
			st = &r.Steps[i]
		}
	}
	if st == nil {
		return nil
	}
	text := fmt.Sprintf("run %s step %d/%d (%s): %s", r.Short(), idx, len(r.Steps), st.Kind, st.Text)
	if st.Check != "" {
		text += fmt.Sprintf(" — evidence: %s", st.Check)
	}
	item, err := desk.Send(p, "work", "gears", agent, text, "run:"+r.ID)
	if err != nil {
		return err
	}
	db, err := store.Open(p)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE run_steps SET desk_item = ? WHERE run_id = ? AND idx = ?`, item.ID, runID, idx)
	return err
}

// Get loads a run by id or unique prefix.
func Get(p *project.Project, key string) (*Run, error) {
	runs, err := list(p, true)
	if err != nil {
		return nil, err
	}
	var hit *Run
	for i := range runs {
		if runs[i].ID == key || strings.HasPrefix(runs[i].ID, key) {
			if hit != nil {
				return nil, fmt.Errorf("run id %q is ambiguous", key)
			}
			hit = &runs[i]
		}
	}
	if hit == nil {
		return nil, fmt.Errorf("no run %q", key)
	}
	return hit, nil
}

// Active returns runs still in progress, oldest first.
func Active(p *project.Project) ([]Run, error) { return list(p, false) }

// Only returns the single active run, or an error naming the choices.
func Only(p *project.Project) (*Run, error) {
	runs, err := Active(p)
	if err != nil {
		return nil, err
	}
	switch len(runs) {
	case 0:
		return nil, fmt.Errorf("no active run; start one: gears runbook start <slug>")
	case 1:
		return &runs[0], nil
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.Short()+" ("+r.Runbook+")")
	}
	return nil, fmt.Errorf("%d active runs; pass --run: %s", len(runs), strings.Join(ids, ", "))
}

func list(p *project.Project, all bool) ([]Run, error) {
	if !store.Exists(p) {
		return nil, nil
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	q := `SELECT id, runbook, target, agent, started_at, ended_at, status FROM runs`
	if !all {
		q += ` WHERE status = 'active'`
	}
	q += ` ORDER BY started_at`
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	var runs []Run
	for rows.Next() {
		var r Run
		var started, ended string
		if err := rows.Scan(&r.ID, &r.Runbook, &r.Target, &r.Agent, &started, &ended, &r.Status); err != nil {
			rows.Close()
			return nil, err
		}
		r.StartedAt = store.Parse(started)
		if ended != "" {
			r.EndedAt = store.Parse(ended)
		}
		runs = append(runs, r)
	}
	rows.Close()
	for i := range runs {
		srows, err := db.Query(`SELECT idx, kind, text, "check", state, evidence, done_at, reason, desk_item FROM run_steps WHERE run_id = ? ORDER BY idx`, runs[i].ID)
		if err != nil {
			return nil, err
		}
		for srows.Next() {
			var s RunStep
			var done string
			if err := srows.Scan(&s.Idx, &s.Kind, &s.Text, &s.Check, &s.State, &s.Evidence, &done, &s.Reason, &s.DeskItem); err != nil {
				srows.Close()
				return nil, err
			}
			if done != "" {
				s.DoneAt = store.Parse(done)
			}
			runs[i].Steps = append(runs[i].Steps, s)
		}
		srows.Close()
	}
	return runs, nil
}

// Outcome is what Complete decided about a step.
type Outcome struct {
	Step     RunStep
	Verified bool
	Note     string
	Finished bool // the run completed with this step
}

// Complete records evidence for the current step, verifies it where the
// step declares a check, and advances. Verification failure is an error:
// the step stays current and nothing is stamped.
func Complete(p *project.Project, runID, evidence, agent string) (*Outcome, error) {
	r, err := Get(p, runID)
	if err != nil {
		return nil, err
	}
	if r.Status != "active" {
		return nil, fmt.Errorf("run %s is %s", r.Short(), r.Status)
	}
	st := r.Current()
	if st == nil {
		return nil, fmt.Errorf("run %s has no current step", r.Short())
	}
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return nil, fmt.Errorf("step %d needs evidence; nothing is marked done without it", st.Idx)
	}
	if agent == "" {
		agent = session.DefaultAgent()
	}

	verified, note, err := verify(p, r, st, evidence)
	if err != nil {
		return nil, fmt.Errorf("step %d not done: %w", st.Idx, err)
	}
	state := "done"
	if verified {
		state = "verified"
	}
	return finishStep(p, r, st, state, evidence, "", agent, verified, note)
}

// Skip marks the current step skipped with a reason and advances. The skip
// stays visible on the run.
func Skip(p *project.Project, runID, reason, agent string) (*Outcome, error) {
	r, err := Get(p, runID)
	if err != nil {
		return nil, err
	}
	if r.Status != "active" {
		return nil, fmt.Errorf("run %s is %s", r.Short(), r.Status)
	}
	st := r.Current()
	if st == nil {
		return nil, fmt.Errorf("run %s has no current step", r.Short())
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("a reason is required to skip a step")
	}
	if agent == "" {
		agent = session.DefaultAgent()
	}
	return finishStep(p, r, st, "skipped", "", strings.TrimSpace(reason), agent, false, "skipped: "+reason)
}

func finishStep(p *project.Project, r *Run, st *RunStep, state, evidence, reason, agent string, verified bool, note string) (*Outcome, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	now := store.Now()
	if _, err := db.Exec(`UPDATE run_steps SET state = ?, evidence = ?, reason = ?, done_at = ? WHERE run_id = ? AND idx = ?`,
		state, evidence, reason, now, r.ID, st.Idx); err != nil {
		db.Close()
		return nil, err
	}
	next := st.Idx + 1
	finished := next > len(r.Steps)
	if finished {
		_, _ = db.Exec(`UPDATE runs SET status = 'completed', ended_at = ? WHERE id = ?`, now, r.ID)
	} else {
		_, _ = db.Exec(`UPDATE run_steps SET state = 'current' WHERE run_id = ? AND idx = ?`, r.ID, next)
	}
	db.Close()

	// Close the desk item for this step; place the next.
	if st.DeskItem != "" {
		_, _ = desk.Done(p, st.DeskItem, agent)
	}
	if !finished {
		if err := placeOnDesk(p, r.ID, next, r.Agent); err != nil {
			return nil, err
		}
	}
	st.State, st.Evidence, st.Reason, st.DoneAt = state, evidence, reason, store.Parse(now)
	return &Outcome{Step: *st, Verified: verified, Note: note, Finished: finished}, nil
}

// Abandon ends a run without completing it and clears its desk item.
func Abandon(p *project.Project, runID, reason, agent string) (*Run, error) {
	r, err := Get(p, runID)
	if err != nil {
		return nil, err
	}
	if r.Status != "active" {
		return nil, fmt.Errorf("run %s is already %s", r.Short(), r.Status)
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("a reason is required to abandon a run")
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	now := store.Now()
	_, err = db.Exec(`UPDATE runs SET status = 'abandoned', ended_at = ? WHERE id = ?`, now, r.ID)
	db.Close()
	if err != nil {
		return nil, err
	}
	if st := r.Current(); st != nil && st.DeskItem != "" {
		_, _ = desk.Done(p, st.DeskItem, agent)
	}
	_, _ = session.Append(p, "problem", agent, fmt.Sprintf("abandoned run %s (%s): %s", r.Short(), r.Runbook, reason))
	return Get(p, r.ID)
}

// verify checks evidence against the step's declared check. Steps without a
// check accept any evidence and are "done", not "verified".
func verify(p *project.Project, r *Run, st *RunStep, evidence string) (verified bool, note string, err error) {
	if st.Check == "" {
		return false, "recorded", nil
	}
	root := p.Root
	switch st.Check {
	case "branch":
		if !gitx.IsRepo(root) {
			return false, "", fmt.Errorf("%s is not a git repository", root)
		}
		if !gitx.BranchExists(root, evidence) {
			return false, "", fmt.Errorf("branch %q does not exist", evidence)
		}
		return true, "branch exists", nil
	case "commit":
		if !gitx.IsRepo(root) {
			return false, "", fmt.Errorf("%s is not a git repository", root)
		}
		if !gitx.CommitExists(root, evidence) {
			return false, "", fmt.Errorf("commit %q not found", evidence)
		}
		// If an earlier step produced a branch, the commit must be on it.
		if br := earlierEvidence(r, "branch"); br != "" && !gitx.CommitOnBranch(root, evidence, br) {
			return false, "", fmt.Errorf("commit %s is not on branch %s", evidence, br)
		}
		return true, "commit exists", nil
	case "pushed":
		if !gitx.IsRepo(root) {
			return false, "", fmt.Errorf("%s is not a git repository", root)
		}
		if !gitx.Pushed(root, evidence) {
			return false, "", fmt.Errorf("origin/%s not found; push the branch first", evidence)
		}
		return true, "origin has the branch", nil
	case "session":
		id, convErr := strconv.ParseInt(strings.TrimPrefix(evidence, "#"), 10, 64)
		if convErr != nil || !session.Exists(p, id) {
			return false, "", fmt.Errorf("session entry %q not found", evidence)
		}
		return true, "session entry exists", nil
	case "desk":
		if !desk.Exists(p, evidence) {
			return false, "", fmt.Errorf("desk item %q not found", evidence)
		}
		return true, "desk item exists", nil
	case "artifact":
		if _, statErr := os.Stat(filepath.Join(p.ArtifactsDir(), evidence)); statErr != nil {
			return false, "", fmt.Errorf("artifact %q not found", evidence)
		}
		return true, "artifact exists", nil
	}
	return false, "", fmt.Errorf("unknown check %q", st.Check)
}

func earlierEvidence(r *Run, check string) string {
	for _, s := range r.Steps {
		if s.Check == check && (s.State == "verified" || s.State == "done") {
			return s.Evidence
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
