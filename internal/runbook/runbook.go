// Package runbook holds the two halves of a process an agent cannot fake:
// the definition (a runbook--*.yaml artifact, authored only here, with a
// fixed step vocabulary) and the run (a record of one execution, bound to a
// card and a branch, with a timestamp and verified evidence per step).
//
// Rules (decided 2026-10-10): one active run per agent; only the agent that
// started a run advances it, unless another takes it over explicitly; an
// active run owns its card's state; evidence is a Gears or git object,
// never a shell command, and is checked against the run (its card, its
// branch, when the step began), not merely for existence.
package runbook

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/card"
	"github.com/Syluxso/gears-cli/internal/desk"
	"github.com/Syluxso/gears-cli/internal/gitx"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
)

// sessionRef is evidence that names an existing entry (its id, or six or more
// hex digits of it, optionally after #) rather than text to log.
var sessionRef = regexp.MustCompile(`^#?[0-9a-fA-F-]{6,36}$`)

// Kinds is the whole step vocabulary.
var Kinds = []string{"ask", "do", "gears", "git", "gate"}

// Targets a runbook may declare. card: a run needs a card and owns its
// state. repo: a run is about the repository. none: free-standing.
var Targets = []string{"card", "repo", "none"}

// Checks a step may declare, by kind. A check names what the evidence is
// and how it is verified against the run. Some take an argument, written
// check:arg (card:Testing, merged:main).
//
//	branch     evidence is a branch name; it must exist; the run binds to it
//	commit     evidence is a commit; it must be on the run's branch and
//	           made after the run started
//	pushed     the run's branch is on origin with every local commit
//	merged     the run's branch is contained in arg (default main)
//	clean      the working tree has no uncommitted changes
//	session    gears: the step logs the evidence text as a session entry
//	desk       evidence is a desk item created after the step began
//	           (gate: it must be closed, i.e. answered or done)
//	artifact   evidence is an artifact file that passes its shape
//	card       the run's card; gears card:State moves it there as the step
//	           (gate card:State waits for it to be there)
var Checks = map[string][]string{
	"ask":   {},
	"do":    {"commit", "clean"},
	"gears": {"session", "desk", "artifact", "card"},
	"git":   {"branch", "commit", "pushed", "merged", "clean"},
	"gate":  {"pushed", "merged", "card", "desk"},
}

// Step is one line of a runbook definition.
type Step struct {
	Kind  string `yaml:"kind"`
	Text  string `yaml:"text"`
	Check string `yaml:"check,omitempty"`
	Arg   string `yaml:"arg,omitempty"`
	// Name lets a later step refer to this step's evidence in when:.
	// Required for ask steps that drive a branch.
	Name string `yaml:"name,omitempty"`
	// When is "<name>=<value>" or "<name>!=<value>" on an earlier step. A
	// step whose condition does not hold is skipped automatically, visibly.
	When string `yaml:"when,omitempty"`
}

// Runbook is the definition file.
type Runbook struct {
	Title   string `yaml:"title"`
	Slug    string `yaml:"slug"`
	Created string `yaml:"created"`
	Target  string `yaml:"target,omitempty"`
	Steps   []Step `yaml:"steps"`
	// Path is where it was loaded from. Not serialised.
	Path string `yaml:"-"`
}

func path(p *project.Project, slug string) string {
	return filepath.Join(p.ArtifactsDir(), "runbook--"+slug+".yaml")
}

// New writes an empty runbook definition.
func New(p *project.Project, title, target string) (*Runbook, error) {
	slug := project.Slugify(title)
	if slug == "" {
		return nil, fmt.Errorf("title %q produces an empty slug", title)
	}
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		target = "none"
	}
	if !contains(Targets, target) {
		return nil, fmt.Errorf("target %q; one of %s", target, strings.Join(Targets, ", "))
	}
	rb := &Runbook{Title: title, Slug: slug, Created: time.Now().Format("2006-01-02"), Target: target, Path: path(p, slug)}
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
	if rb.Target == "" {
		rb.Target = "none"
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

// SplitCheck parses "card:Testing" into check and arg.
func SplitCheck(s string) (check, arg string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return strings.ToLower(strings.TrimSpace(s[:i])), strings.TrimSpace(s[i+1:])
	}
	return strings.ToLower(s), ""
}

// AddStep appends a step after validating kind, check, argument and when.
func (rb *Runbook) AddStep(kind, text, check, arg, name, when string) error {
	s, err := rb.validateStep(Step{Kind: kind, Text: text, Check: check, Arg: arg, Name: name, When: when}, len(rb.Steps))
	if err != nil {
		return err
	}
	rb.Steps = append(rb.Steps, s)
	return rb.save()
}

func parseWhen(w string) (name, op, value string, err error) {
	if i := strings.Index(w, "!="); i > 0 {
		return strings.TrimSpace(w[:i]), "!=", strings.TrimSpace(w[i+2:]), nil
	}
	if i := strings.IndexByte(w, '='); i > 0 {
		return strings.TrimSpace(w[:i]), "=", strings.TrimSpace(w[i+1:]), nil
	}
	return "", "", "", fmt.Errorf("when %q: want <name>=<value> or <name>!=<value>", w)
}

func (rb *Runbook) save() error {
	out, err := yaml.Marshal(rb)
	if err != nil {
		return err
	}
	header := "# Runbook. Authored with gears runbook; edit through it so the shape stays finite.\n"
	return os.WriteFile(rb.Path, append([]byte(header), out...), 0o644)
}

// ------------------------------------------------------------------- run

// Run is one execution of a runbook.
type Run struct {
	ID        string
	Runbook   string
	Target    string
	CardKey   string
	CardID    string
	Branch    string
	Agent     string
	StartedAt time.Time
	EndedAt   time.Time
	Status    string // active | completed | abandoned
	Steps     []RunStep
}

// RunStep is a step copied into a run, with its outcome.
type RunStep struct {
	Idx       int
	Kind      string
	Text      string
	Check     string
	Arg       string
	Name      string
	When      string
	State     string // pending | current | done | verified | skipped
	Evidence  string
	StartedAt time.Time
	DoneAt    time.Time
	Reason    string
	DeskItem  string
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

// Label is how a step's check reads: "card:Testing".
func (s RunStep) Label() string {
	if s.Arg != "" {
		return s.Check + ":" + s.Arg
	}
	return s.Check
}

// ActiveFor returns the agent's active run, if any.
func ActiveFor(p *project.Project, agent string) (*Run, error) {
	runs, err := Active(p)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		if runs[i].Agent == agent {
			return &runs[i], nil
		}
	}
	return nil, nil
}

// Start creates a run from a runbook, binds it to its card, copies the
// steps, and places the first step on the agent's desk as a work item.
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
	if mine, err := ActiveFor(p, agent); err != nil {
		return nil, err
	} else if mine != nil {
		return nil, fmt.Errorf("%s already has an active run %s (%s); finish or abandon it first", agent, mine.Short(), mine.Runbook)
	}
	target = strings.TrimSpace(target)
	cardKey, cardID := "", ""
	if rb.Target == "card" {
		if target == "" {
			return nil, fmt.Errorf("runbook %s runs against a card; pass its key as the target", slug)
		}
		cd, err := card.Get(p, target)
		if err != nil {
			return nil, err
		}
		if cd.CompletedAt.After(time.Time{}) {
			return nil, fmt.Errorf("%s is completed", cd.Key)
		}
		if id, who := card.ActiveRun(p, cd.ID); id != "" {
			return nil, fmt.Errorf("%s already has an active run %s by %s", cd.Key, id[:8], who)
		}
		cardKey, cardID, target = cd.Key, cd.ID, cd.Key
	} else if rb.Target == "repo" && !gitx.IsRepo(p.Root) {
		return nil, fmt.Errorf("runbook %s runs against a repository; %s is not one", slug, p.Root)
	}

	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	r := &Run{ID: project.NewID(), Runbook: slug, Target: target, CardKey: cardKey, CardID: cardID, Agent: agent, Status: "active"}
	now := store.Now()
	if _, err := db.Exec(`INSERT INTO runs (id, runbook, target, card_id, branch, agent, started_at, status) VALUES (?, ?, ?, ?, '', ?, ?, 'active')`,
		r.ID, r.Runbook, r.Target, r.CardID, r.Agent, now); err != nil {
		db.Close()
		return nil, err
	}
	for i, s := range rb.Steps {
		if _, err := db.Exec(`INSERT INTO run_steps (run_id, idx, kind, text, "check", arg, name, when_cond, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending')`,
			r.ID, i+1, s.Kind, s.Text, s.Check, s.Arg, s.Name, s.When); err != nil {
			db.Close()
			return nil, err
		}
	}
	db.Close()
	if cardID != "" {
		if _, err := card.Update(p, cardID, card.Patch{Assignee: &agent}, agent); err != nil {
			return nil, err
		}
		_ = card.Note(p, cardID, agent, "run", "started "+slug+" ("+r.ID[:8]+")")
	}
	if _, err := advance(p, r.ID, 0, agent); err != nil {
		return nil, err
	}
	return Get(p, r.ID)
}

// advance makes the step after idx current, skipping steps whose when:
// does not hold, and places the current step on the desk. It returns true
// when the run finished instead.
func advance(p *project.Project, runID string, idx int, agent string) (bool, error) {
	for {
		r, err := Get(p, runID)
		if err != nil {
			return false, err
		}
		next := idx + 1
		if next > len(r.Steps) {
			return true, finish(p, r)
		}
		st := r.Steps[next-1]
		if st.When != "" && !holds(r, st.When) {
			db, err := store.Open(p)
			if err != nil {
				return false, err
			}
			_, err = db.Exec(`UPDATE run_steps SET state = 'skipped', reason = ?, done_at = ? WHERE run_id = ? AND idx = ?`,
				"when "+st.When+" did not hold", store.Now(), runID, next)
			db.Close()
			if err != nil {
				return false, err
			}
			idx = next
			continue
		}
		db, err := store.Open(p)
		if err != nil {
			return false, err
		}
		_, err = db.Exec(`UPDATE run_steps SET state = 'current', started_at = ? WHERE run_id = ? AND idx = ?`, store.Now(), runID, next)
		db.Close()
		if err != nil {
			return false, err
		}
		return false, placeOnDesk(p, runID, next, agent)
	}
}

func holds(r *Run, when string) bool {
	name, op, value, err := parseWhen(when)
	if err != nil {
		return true
	}
	got := ""
	for _, s := range r.Steps {
		if s.Name == name {
			got = s.Evidence
		}
	}
	eq := strings.EqualFold(strings.TrimSpace(got), value)
	if op == "!=" {
		return !eq
	}
	return eq
}

func finish(p *project.Project, r *Run) error {
	db, err := store.Open(p)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE runs SET status = 'completed', ended_at = ? WHERE id = ?`, store.Now(), r.ID)
	db.Close()
	if err != nil {
		return err
	}
	if r.CardID != "" {
		_ = card.Note(p, r.CardID, r.Agent, "run", "completed "+r.Runbook+" ("+r.Short()+")")
	}
	return nil
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
		text += " [" + st.Label() + "]"
	}
	ref := "run:" + r.ID
	if r.CardKey != "" {
		ref = r.CardKey
	}
	item, err := desk.Send(p, "work", "gears", agent, text, ref)
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

// Only returns the agent's active run, else the single active run, else an
// error naming the choices.
func Only(p *project.Project, agent string) (*Run, error) {
	if agent != "" {
		if mine, err := ActiveFor(p, agent); err != nil {
			return nil, err
		} else if mine != nil {
			return mine, nil
		}
	}
	runs, err := Active(p)
	if err != nil {
		return nil, err
	}
	switch len(runs) {
	case 0:
		return nil, fmt.Errorf("no active run; start one: gears runbook start <slug> --target <card>")
	case 1:
		return &runs[0], nil
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.Short()+" ("+r.Runbook+", "+r.Agent+")")
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
	q := `SELECT r.id, r.runbook, r.target, r.card_id, COALESCE(c.key, ''), r.branch, r.agent, r.started_at, r.ended_at, r.status
		FROM runs r LEFT JOIN cards c ON c.id = r.card_id`
	if !all {
		q += ` WHERE r.status = 'active'`
	}
	q += ` ORDER BY r.started_at`
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	var runs []Run
	for rows.Next() {
		var r Run
		var started, ended string
		if err := rows.Scan(&r.ID, &r.Runbook, &r.Target, &r.CardID, &r.CardKey, &r.Branch, &r.Agent, &started, &ended, &r.Status); err != nil {
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
		srows, err := db.Query(`SELECT idx, kind, text, "check", arg, name, when_cond, state, evidence, started_at, done_at, reason, desk_item FROM run_steps WHERE run_id = ? ORDER BY idx`, runs[i].ID)
		if err != nil {
			return nil, err
		}
		for srows.Next() {
			var s RunStep
			var started, done string
			if err := srows.Scan(&s.Idx, &s.Kind, &s.Text, &s.Check, &s.Arg, &s.Name, &s.When, &s.State, &s.Evidence, &started, &done, &s.Reason, &s.DeskItem); err != nil {
				srows.Close()
				return nil, err
			}
			if started != "" {
				s.StartedAt = store.Parse(started)
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

func ownerCheck(r *Run, agent string) error {
	if r.Status != "active" {
		return fmt.Errorf("run %s is %s", r.Short(), r.Status)
	}
	if r.Agent != agent {
		return fmt.Errorf("run %s belongs to %s; the local run engine is retired, so it cannot be continued as %s", r.Short(), r.Agent, agent)
	}
	return nil
}

// Complete performs or verifies the current step with the evidence given
// and advances. Verification failure is an error: the step stays current
// and nothing is stamped.
func Complete(p *project.Project, runID, evidence, agent string) (*Outcome, error) {
	if agent == "" {
		agent = session.DefaultAgent()
	}
	r, err := Get(p, runID)
	if err != nil {
		return nil, err
	}
	if err := ownerCheck(r, agent); err != nil {
		return nil, err
	}
	st := r.Current()
	if st == nil {
		return nil, fmt.Errorf("run %s has no current step", r.Short())
	}
	evidence = strings.TrimSpace(evidence)
	res, err := perform(p, r, st, evidence, agent)
	if err != nil {
		return nil, fmt.Errorf("step %d not done: %w", st.Idx, err)
	}
	if res.branch != "" && r.Branch == "" {
		db, err := store.Open(p)
		if err != nil {
			return nil, err
		}
		_, _ = db.Exec(`UPDATE runs SET branch = ? WHERE id = ?`, res.branch, r.ID)
		db.Close()
	}
	state := "done"
	if res.verified {
		state = "verified"
	}
	return finishStep(p, r, st, state, res.evidence, "", agent, res.verified, res.note)
}

// Skip marks the current step skipped with a reason and advances. The skip
// stays visible on the run.
func Skip(p *project.Project, runID, reason, agent string) (*Outcome, error) {
	if agent == "" {
		agent = session.DefaultAgent()
	}
	r, err := Get(p, runID)
	if err != nil {
		return nil, err
	}
	if err := ownerCheck(r, agent); err != nil {
		return nil, err
	}
	st := r.Current()
	if st == nil {
		return nil, fmt.Errorf("run %s has no current step", r.Short())
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("a reason is required to skip a step")
	}
	return finishStep(p, r, st, "skipped", "", strings.TrimSpace(reason), agent, false, "skipped: "+reason)
}

func finishStep(p *project.Project, r *Run, st *RunStep, state, evidence, reason, agent string, verified bool, note string) (*Outcome, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	now := store.Now()
	_, err = db.Exec(`UPDATE run_steps SET state = ?, evidence = ?, reason = ?, done_at = ? WHERE run_id = ? AND idx = ?`,
		state, evidence, reason, now, r.ID, st.Idx)
	db.Close()
	if err != nil {
		return nil, err
	}
	if st.DeskItem != "" {
		_, _ = desk.Done(p, st.DeskItem, agent)
	}
	if r.CardID != "" {
		_ = card.Note(p, r.CardID, agent, "run", fmt.Sprintf("step %d %s: %s %s", st.Idx, state, st.Text, note))
	}
	finished, err := advance(p, r.ID, st.Idx, r.Agent)
	if err != nil {
		return nil, err
	}
	st.State, st.Evidence, st.Reason, st.DoneAt = state, evidence, reason, store.Parse(now)
	return &Outcome{Step: *st, Verified: verified, Note: note, Finished: finished}, nil
}

// Takeover moves an active run to another agent, with a reason that lands
// on the session log and the card. The current step's desk item moves too.
func Takeover(p *project.Project, runID, agent, reason string) (*Run, error) {
	if agent == "" {
		agent = session.DefaultAgent()
	}
	r, err := Get(p, runID)
	if err != nil {
		return nil, err
	}
	if r.Status != "active" {
		return nil, fmt.Errorf("run %s is %s", r.Short(), r.Status)
	}
	if r.Agent == agent {
		return r, nil
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("a reason is required to take over a run")
	}
	if mine, err := ActiveFor(p, agent); err != nil {
		return nil, err
	} else if mine != nil {
		return nil, fmt.Errorf("%s already has an active run %s; finish or abandon it first", agent, mine.Short())
	}
	from := r.Agent
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`UPDATE runs SET agent = ? WHERE id = ?`, agent, r.ID)
	db.Close()
	if err != nil {
		return nil, err
	}
	if st := r.Current(); st != nil {
		if st.DeskItem != "" {
			_, _ = desk.Done(p, st.DeskItem, agent)
		}
		if err := placeOnDesk(p, r.ID, st.Idx, agent); err != nil {
			return nil, err
		}
	}
	_, _ = session.Append(p, "decision", agent, fmt.Sprintf("took over run %s (%s) from %s: %s", r.Short(), r.Runbook, from, reason))
	if r.CardID != "" {
		_, _ = card.Update(p, r.CardID, card.Patch{Assignee: &agent}, agent)
		_ = card.Note(p, r.CardID, agent, "run", "taken over from "+from+": "+reason)
	}
	return Get(p, r.ID)
}

// Abandon ends a run without completing it and clears its desk item. The
// card stays where it is, unlocked.
func Abandon(p *project.Project, runID, reason, agent string) (*Run, error) {
	if agent == "" {
		agent = session.DefaultAgent()
	}
	r, err := Get(p, runID)
	if err != nil {
		return nil, err
	}
	if err := ownerCheck(r, agent); err != nil {
		return nil, err
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
	if r.CardID != "" {
		_ = card.Note(p, r.CardID, agent, "run", "abandoned "+r.Runbook+": "+reason)
	}
	return Get(p, r.ID)
}

// --------------------------------------------------------------- perform

type result struct {
	verified bool
	note     string
	evidence string
	branch   string // learned from a branch step
}

// perform is the step. ask records; gears acts; git, do and gate verify.
// Each check is bound to the run: its branch, its card, when the step began.
func perform(p *project.Project, r *Run, st *RunStep, evidence, agent string) (*result, error) {
	root := p.Root
	needRepo := func() error {
		if !gitx.IsRepo(root) {
			return fmt.Errorf("%s is not a git repository", root)
		}
		return nil
	}
	if st.Check == "" {
		if evidence == "" {
			return nil, fmt.Errorf("say what happened; nothing is marked done without a note")
		}
		if st.Kind == "ask" {
			return &result{note: "answered", evidence: evidence}, nil
		}
		return &result{note: "recorded", evidence: evidence}, nil
	}
	switch st.Check {
	case "branch":
		if err := needRepo(); err != nil {
			return nil, err
		}
		if evidence == "" {
			evidence = gitx.CurrentBranch(root)
		}
		if !gitx.BranchExists(root, evidence) {
			return nil, fmt.Errorf("branch %q does not exist", evidence)
		}
		if r.Branch != "" && r.Branch != evidence {
			return nil, fmt.Errorf("this run is bound to branch %s", r.Branch)
		}
		if r.CardKey != "" && !strings.Contains(strings.ToLower(evidence), strings.ToLower(r.CardKey)) {
			return nil, fmt.Errorf("branch %q does not name the card %s", evidence, r.CardKey)
		}
		return &result{verified: true, note: "branch exists", evidence: evidence, branch: evidence}, nil
	case "commit":
		if err := needRepo(); err != nil {
			return nil, err
		}
		if evidence == "" {
			evidence = "HEAD"
		}
		sha := gitx.Resolve(root, evidence)
		if sha == "" {
			return nil, fmt.Errorf("commit %q not found", evidence)
		}
		if r.Branch != "" && !gitx.CommitOnBranch(root, sha, r.Branch) {
			return nil, fmt.Errorf("commit %s is not on the run's branch %s", sha[:8], r.Branch)
		}
		if t, ok := gitx.CommitTime(root, sha); ok && t.Before(r.StartedAt) {
			return nil, fmt.Errorf("commit %s predates this run (%s)", sha[:8], t.Local().Format("15:04"))
		}
		return &result{verified: true, note: "commit on " + orAny(r.Branch) + " after run start", evidence: sha[:12]}, nil
	case "clean":
		if err := needRepo(); err != nil {
			return nil, err
		}
		if !gitx.Clean(root) {
			return nil, fmt.Errorf("working tree has uncommitted changes")
		}
		return &result{verified: true, note: "working tree clean", evidence: "clean"}, nil
	case "pushed":
		if err := needRepo(); err != nil {
			return nil, err
		}
		br := r.Branch
		if br == "" {
			br = evidence
		}
		if br == "" {
			return nil, fmt.Errorf("no branch bound to this run; give the branch name")
		}
		if !gitx.Pushed(root, br) {
			return nil, fmt.Errorf("origin/%s is missing or behind; push the branch", br)
		}
		return &result{verified: true, note: "origin has every commit of " + br, evidence: br}, nil
	case "merged":
		if err := needRepo(); err != nil {
			return nil, err
		}
		br := r.Branch
		if br == "" {
			br = evidence
		}
		if br == "" {
			return nil, fmt.Errorf("no branch bound to this run; give the branch name")
		}
		if !gitx.Merged(root, br, st.Arg) {
			return nil, fmt.Errorf("%s is not merged into %s", br, st.Arg)
		}
		return &result{verified: true, note: br + " merged into " + st.Arg, evidence: br}, nil
	case "session":
		if evidence == "" {
			return nil, fmt.Errorf("give the text to log")
		}
		if sessionRef.MatchString(evidence) {
			e, err := session.Find(p, evidence)
			if err != nil {
				return nil, err
			}
			if e.Agent != r.Agent {
				return nil, fmt.Errorf("session entry #%s was written by %s, not this run's agent", e.Short(), e.Agent)
			}
			if e.At.Before(st.StartedAt) {
				return nil, fmt.Errorf("session entry #%s predates this step", e.Short())
			}
			return &result{verified: true, note: "session entry exists", evidence: e.ID}, nil
		}
		e, err := session.Append(p, "done", agent, evidence)
		if err != nil {
			return nil, err
		}
		return &result{verified: true, note: "logged session entry", evidence: e.ID}, nil
	case "desk":
		if evidence == "" {
			return nil, fmt.Errorf("give the desk item id")
		}
		it, err := desk.Get(p, evidence)
		if err != nil {
			return nil, err
		}
		if it.CreatedAt.Before(st.StartedAt) && st.Kind != "gate" {
			return nil, fmt.Errorf("desk item %s predates this step", it.Short())
		}
		if st.Kind == "gate" && it.State != "closed" {
			return nil, fmt.Errorf("waiting: desk item %s (%s) is still open", it.Short(), it.Type)
		}
		return &result{verified: true, note: "desk item " + it.Type + " " + it.State, evidence: it.ID}, nil
	case "artifact":
		if evidence == "" {
			return nil, fmt.Errorf("give the artifact file name")
		}
		a, err := artifact.Find(p, evidence)
		if err != nil {
			return nil, err
		}
		if !a.FilePresent {
			return nil, fmt.Errorf("artifact %s has no file", a.File)
		}
		if errs := artifact.Validate(p, *a); len(errs) > 0 {
			return nil, fmt.Errorf("artifact %s fails its shape: %s", a.File, strings.Join(errs, "; "))
		}
		return &result{verified: true, note: "artifact passes its shape", evidence: a.File}, nil
	case "card":
		if r.CardID == "" {
			return nil, fmt.Errorf("this run has no card")
		}
		if st.Kind == "gears" && st.Arg != "" {
			cd, _, err := card.MoveForRun(p, r.CardID, st.Arg, agent)
			if err != nil {
				return nil, err
			}
			return &result{verified: true, note: "card moved to " + cd.State, evidence: cd.Key + " in " + cd.State}, nil
		}
		cd, err := card.Get(p, r.CardID)
		if err != nil {
			return nil, err
		}
		if st.Arg != "" && !strings.EqualFold(cd.State, st.Arg) {
			return nil, fmt.Errorf("waiting: %s is in %s, not %s", cd.Key, cd.State, st.Arg)
		}
		return &result{verified: true, note: cd.Key + " in " + cd.State, evidence: cd.Key + " in " + cd.State}, nil
	}
	return nil, fmt.Errorf("unknown check %q", st.Check)
}

func orAny(s string) string {
	if s == "" {
		return "any branch"
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
