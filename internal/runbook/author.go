package runbook

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Syluxso/gears-cli/internal/project"
)

// Authoring beyond append. Every edit revalidates the whole runbook, so a
// step that moves or disappears cannot leave a when: pointing at nothing or
// at a later step. A runbook in use by an active run is frozen: runs copy
// their steps at start, but a definition that changes under a run would
// make its evidence read against a document that no longer says that.

// validateStep checks one step against the steps before it (for when:) and
// every other step (for name uniqueness). It normalises kind, check, arg.
func (rb *Runbook) validateStep(s Step, pos int) (Step, error) {
	s.Kind = strings.ToLower(strings.TrimSpace(s.Kind))
	if !contains(Kinds, s.Kind) {
		return s, fmt.Errorf("kind %q; one of %s", s.Kind, strings.Join(Kinds, ", "))
	}
	s.Text = strings.TrimSpace(s.Text)
	if s.Text == "" {
		return s, fmt.Errorf("step text is empty")
	}
	if c, a := SplitCheck(s.Check); a != "" {
		s.Check, s.Arg = c, a
	} else {
		s.Check = c
	}
	s.Arg = strings.TrimSpace(s.Arg)
	if s.Check != "" && !contains(Checks[s.Kind], s.Check) {
		allowed := Checks[s.Kind]
		if len(allowed) == 0 {
			return s, fmt.Errorf("kind %s takes no check", s.Kind)
		}
		return s, fmt.Errorf("check %q for kind %s; one of %s", s.Check, s.Kind, strings.Join(allowed, ", "))
	}
	if s.Kind == "gate" && s.Check == "" {
		return s, fmt.Errorf("a gate needs a check to wait on: %s", strings.Join(Checks["gate"], ", "))
	}
	if s.Check == "card" && rb.Target != "card" {
		return s, fmt.Errorf("check card needs a runbook with target card (this one is %s)", rb.Target)
	}
	if s.Kind == "gate" && s.Check == "card" && s.Arg == "" {
		return s, fmt.Errorf("gate card needs a state: card:<State>")
	}
	if s.Check == "merged" && s.Arg == "" {
		s.Arg = "main"
	}
	if s.Arg != "" && s.Check != "card" && s.Check != "merged" {
		return s, fmt.Errorf("check %s takes no argument", s.Check)
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name != "" {
		if project.Slugify(s.Name) != s.Name {
			return s, fmt.Errorf("name %q: lowercase letters, digits and dashes", s.Name)
		}
		for i, o := range rb.Steps {
			if i != pos && o.Name == s.Name {
				return s, fmt.Errorf("name %q is already used by step %d", s.Name, i+1)
			}
		}
	}
	s.When = strings.TrimSpace(s.When)
	if s.When != "" {
		ref, _, _, err := parseWhen(s.When)
		if err != nil {
			return s, err
		}
		found := false
		for i := 0; i < pos && i < len(rb.Steps); i++ {
			if rb.Steps[i].Name == ref {
				found = true
			}
		}
		if !found {
			return s, fmt.Errorf("step %d: when refers to %q; no earlier step has that name", pos+1, ref)
		}
	}
	return s, nil
}

// validateAll re-checks every step in order.
func (rb *Runbook) validateAll() error {
	for i := range rb.Steps {
		s, err := rb.validateStep(rb.Steps[i], i)
		if err != nil {
			return err
		}
		rb.Steps[i] = s
	}
	return nil
}

func (rb *Runbook) index(idx int) (int, error) {
	if idx < 1 || idx > len(rb.Steps) {
		return 0, fmt.Errorf("step %d: runbook %s has %d step(s)", idx, rb.Slug, len(rb.Steps))
	}
	return idx - 1, nil
}

// frozen refuses edits while any active run is on this runbook.
func frozen(p *project.Project, slug string) error {
	runs, err := Active(p)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Runbook == slug {
			return fmt.Errorf("runbook %s is in use by active run %s (%s); finish or abandon it before editing", slug, r.Short(), r.Agent)
		}
	}
	return nil
}

// StepPatch carries the fields an update may change; nil means keep.
type StepPatch struct {
	Kind  *string
	Text  *string
	Check *string // "" clears the check and arg
	Name  *string // "" clears the name
	When  *string // "" clears the condition
}

// UpdateStep changes one step in place. Renaming a step that a later when:
// refers to is refused; clear or retarget the condition first.
func (rb *Runbook) UpdateStep(p *project.Project, idx int, patch StepPatch) error {
	if err := frozen(p, rb.Slug); err != nil {
		return err
	}
	i, err := rb.index(idx)
	if err != nil {
		return err
	}
	s := rb.Steps[i]
	if patch.Kind != nil {
		s.Kind = *patch.Kind
	}
	if patch.Text != nil {
		s.Text = *patch.Text
	}
	if patch.Check != nil {
		s.Check, s.Arg = *patch.Check, ""
	}
	if patch.Name != nil && strings.TrimSpace(*patch.Name) != s.Name {
		if users := rb.referrers(s.Name, i); len(users) > 0 {
			return fmt.Errorf("step %d is named %q and step(s) %s refer to it in when:; change those first", idx, s.Name, joinInts(users))
		}
		s.Name = *patch.Name
	}
	if patch.When != nil {
		s.When = *patch.When
	}
	s, err = rb.validateStep(s, i)
	if err != nil {
		return err
	}
	rb.Steps[i] = s
	return rb.save()
}

// RemoveStep deletes a step. Refused when a later when: refers to it.
func (rb *Runbook) RemoveStep(p *project.Project, idx int) error {
	if err := frozen(p, rb.Slug); err != nil {
		return err
	}
	i, err := rb.index(idx)
	if err != nil {
		return err
	}
	if users := rb.referrers(rb.Steps[i].Name, i); len(users) > 0 {
		return fmt.Errorf("step %d is named %q and step(s) %s refer to it in when:; change those first", idx, rb.Steps[i].Name, joinInts(users))
	}
	rb.Steps = append(rb.Steps[:i], rb.Steps[i+1:]...)
	if err := rb.validateAll(); err != nil {
		return err
	}
	return rb.save()
}

// MoveStep moves step idx to position to (1-based). Refused when the new
// order would put a when: before the step it refers to.
func (rb *Runbook) MoveStep(p *project.Project, idx, to int) error {
	if err := frozen(p, rb.Slug); err != nil {
		return err
	}
	i, err := rb.index(idx)
	if err != nil {
		return err
	}
	j, err := rb.index(to)
	if err != nil {
		return err
	}
	if i == j {
		return nil
	}
	before := append([]Step(nil), rb.Steps...)
	s := rb.Steps[i]
	rest := append(append([]Step(nil), rb.Steps[:i]...), rb.Steps[i+1:]...)
	rb.Steps = append(append(append([]Step(nil), rest[:j]...), s), rest[j:]...)
	if err := rb.validateAll(); err != nil {
		rb.Steps = before
		return fmt.Errorf("move would break a when: %w", err)
	}
	return rb.save()
}

// referrers lists the 1-based steps whose when: names this step.
func (rb *Runbook) referrers(name string, self int) []int {
	if name == "" {
		return nil
	}
	var out []int
	for i, s := range rb.Steps {
		if i == self || s.When == "" {
			continue
		}
		if ref, _, _, err := parseWhen(s.When); err == nil && ref == name {
			out = append(out, i+1)
		}
	}
	return out
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, ", ")
}

// Update changes the title and/or target. The slug (and file name) stays:
// runs and desk items refer to it. A target change is validated against the
// steps (card checks need target card).
func (rb *Runbook) Update(p *project.Project, title, target string) error {
	if err := frozen(p, rb.Slug); err != nil {
		return err
	}
	if t := strings.TrimSpace(title); t != "" {
		rb.Title = t
	}
	if t := strings.ToLower(strings.TrimSpace(target)); t != "" {
		if !contains(Targets, t) {
			return fmt.Errorf("target %q; one of %s", t, strings.Join(Targets, ", "))
		}
		old := rb.Target
		rb.Target = t
		if err := rb.validateAll(); err != nil {
			rb.Target = old
			return err
		}
	}
	return rb.save()
}

// Delete removes a runbook definition. Refused while a run is active on it;
// completed and abandoned runs keep their copied steps and still read.
func Delete(p *project.Project, slug string) error {
	rb, err := Load(p, slug)
	if err != nil {
		return err
	}
	if err := frozen(p, slug); err != nil {
		return err
	}
	return os.Remove(rb.Path)
}

// ------------------------------------------------------------- copy

// Builtins are runbooks shipped with the CLI. A project gets one with
// gears runbook copy <slug>; from then on it is the project's own file.
var Builtins = map[string]Runbook{
	"single-ticket-development": {
		Title:  "Single ticket development",
		Target: "card",
		Steps: []Step{
			{Kind: "gears", Text: "Take the card", Check: "card", Arg: "In Progress"},
			{Kind: "git", Text: "Branch from main, named after the card", Check: "branch"},
			{Kind: "do", Text: "Implement; commit on the branch", Check: "commit"},
			{Kind: "gears", Text: "Log what was done", Check: "session"},
			{Kind: "ask", Text: "Does this need a review before merge? yes/no", Name: "review"},
			{Kind: "gate", Text: "Wait for the push", Check: "pushed", When: "review=yes"},
			{Kind: "gears", Text: "Hand the card to testing", Check: "card", Arg: "Testing"},
		},
	},
	"decision-record": {
		Title:  "Decision record",
		Target: "none",
		Steps: []Step{
			{Kind: "ask", Text: "What is being decided, in one line?", Name: "question"},
			{Kind: "gears", Text: "Write the ADR and fill its sections", Check: "artifact"},
			{Kind: "gears", Text: "Log the decision", Check: "session"},
			{Kind: "do", Text: "Commit the ADR", Check: "commit"},
		},
	},
}

// BuiltinSlugs lists the shipped runbooks, sorted.
func BuiltinSlugs() []string {
	out := make([]string, 0, len(Builtins))
	for k := range Builtins {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Copy brings a runbook into this project. from is another project's root
// (or any directory holding .gears/artifacts/runbook--<slug>.yaml); empty
// means the built-in of that slug. as renames the copy; empty keeps the slug.
// The copy is validated as if authored here, and refused if the slug exists.
func Copy(p *project.Project, slug, from, as string) (*Runbook, error) {
	slug = strings.TrimSpace(slug)
	var src Runbook
	if from = strings.TrimSpace(from); from == "" {
		b, ok := Builtins[slug]
		if !ok {
			return nil, fmt.Errorf("no built-in runbook %q; built-ins: %s", slug, strings.Join(BuiltinSlugs(), ", "))
		}
		src = b
		src.Slug = slug
	} else {
		file := filepath.Join(from, project.Dir, project.ArtifactsDir, "runbook--"+slug+".yaml")
		if _, err := os.Stat(file); err != nil {
			file = filepath.Join(from, "runbook--"+slug+".yaml")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("no runbook %q under %s", slug, from)
		}
		if err := yaml.Unmarshal(data, &src); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if src.Slug == "" {
			src.Slug = slug
		}
	}
	if as = strings.TrimSpace(as); as != "" {
		src.Slug = project.Slugify(as)
		src.Title = as
	}
	if src.Slug == "" {
		return nil, fmt.Errorf("copy produces an empty slug")
	}
	if src.Target == "" {
		src.Target = "none"
	}
	if src.Title == "" {
		src.Title = src.Slug
	}
	rb := &Runbook{Title: src.Title, Slug: src.Slug, Created: time.Now().Format("2006-01-02"), Target: src.Target, Steps: src.Steps, Path: path(p, src.Slug)}
	if _, err := os.Stat(rb.Path); err == nil {
		return nil, fmt.Errorf("runbook--%s.yaml already exists; pass as to copy under another name", rb.Slug)
	}
	if err := rb.validateAll(); err != nil {
		return nil, fmt.Errorf("copy of %s: %w", slug, err)
	}
	if err := os.MkdirAll(p.ArtifactsDir(), 0o755); err != nil {
		return nil, err
	}
	return rb, rb.save()
}
