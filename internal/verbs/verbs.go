// Package verbs is the one contract both doors share. Every operation an
// agent can perform is a function here with a typed input and a typed,
// JSON-shaped output. The MCP server registers these as tools; the CLI
// renders them as text or --json. Rules live below this layer, in the
// domain packages; this layer names them once and documents them once, so
// the two doors cannot drift.
package verbs

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/card"
	"github.com/Syluxso/gears-cli/internal/desk"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/runbook"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/shape"
)

// Ctx is what every verb needs: the bound project and the acting agent.
type Ctx struct {
	Project *project.Project
	Agent   string
}

func (c Ctx) agent() string {
	if c.Agent != "" {
		return c.Agent
	}
	return session.DefaultAgent()
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// ---------------------------------------------------------------- status

// StatusIn has no fields; the agent comes from Ctx.
type StatusIn struct{}

// StatusOut is the cold-start packet.
type StatusOut struct {
	Project    string        `json:"project"`
	ProjectID  string        `json:"projectId"`
	Root       string        `json:"root"`
	Agent      string        `json:"agent"`
	Prefix     string        `json:"prefix"`
	Desk       []DeskItem    `json:"desk"`
	Cards      CardStats     `json:"cards"`
	Runs       []RunSummary  `json:"runs"`
	Artifacts  ArtifactStats `json:"artifacts"`
	Session    []SessionLine `json:"sessionToday"`
	NextAction string        `json:"nextAction"`
}

// CardStats is the board at a glance: open cards by state, in board order,
// and the ready cards (default state) an agent can pick up.
type CardStats struct {
	Open    int         `json:"open"`
	ByState []CardState `json:"byState"`
	Mine    []CardLine  `json:"mine"`  // assigned to this agent, not completed
	Ready   []CardLine  `json:"ready"` // in the default state, unassigned
}

type CardLine struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	State    string `json:"state,omitempty"`
	Priority string `json:"priority"`
	Title    string `json:"title"`
}

type ArtifactStats struct {
	Total    int            `json:"total"`
	ByPrefix map[string]int `json:"byPrefix"`
	Problems []string       `json:"problems"`
}

// Status assembles everything an agent needs before it starts.
func Status(c Ctx, _ StatusIn) (*StatusOut, error) {
	out := &StatusOut{Project: c.Project.Slug, ProjectID: c.Project.ID, Root: c.Project.Root, Agent: c.agent(), Prefix: c.Project.Prefix,
		Desk: []DeskItem{}, Runs: []RunSummary{}, Session: []SessionLine{},
		Cards:     CardStats{ByState: []CardState{}, Mine: []CardLine{}, Ready: []CardLine{}},
		Artifacts: ArtifactStats{ByPrefix: map[string]int{}, Problems: []string{}}}

	items, err := desk.List(c.Project, desk.Filter{Agent: out.Agent})
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		out.Desk = append(out.Desk, deskItem(it))
	}

	runs, err := runbook.Active(c.Project)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		out.Runs = append(out.Runs, runSummary(r))
	}

	cards, err := card.List(c.Project, card.Filter{})
	if err != nil {
		return nil, err
	}
	if len(cards) > 0 {
		sts, err := card.States(c.Project)
		if err != nil {
			return nil, err
		}
		counts := map[string]int{}
		for _, cd := range cards {
			counts[cd.State]++
			if cd.Assignee == out.Agent {
				out.Cards.Mine = append(out.Cards.Mine, CardLine{cd.Key, cd.Type, cd.State, "P" + itoa(cd.Priority), cd.Title})
			}
		}
		for _, s := range sts {
			if s.Terminal {
				continue
			}
			out.Cards.ByState = append(out.Cards.ByState, CardState{Name: s.Name, Count: counts[s.Name], Default: s.Default, WIPLimit: s.WIPLimit})
			out.Cards.Open += counts[s.Name]
			if s.Default {
				// Highest priority first; List already orders by position.
				for pri := 3; pri >= 0; pri-- {
					for _, cd := range cards {
						if cd.State == s.Name && cd.Assignee == "" && cd.Priority == pri {
							out.Cards.Ready = append(out.Cards.Ready, CardLine{cd.Key, cd.Type, "", "P" + itoa(pri), cd.Title})
						}
					}
				}
			}
		}
	}

	arts, err := artifact.List(c.Project)
	if err != nil {
		return nil, err
	}
	out.Artifacts.Total = len(arts)
	for _, a := range arts {
		out.Artifacts.ByPrefix[a.Prefix]++
		switch {
		case !a.FilePresent:
			out.Artifacts.Problems = append(out.Artifacts.Problems, a.File+": file missing")
		case !a.ShapeOK:
			out.Artifacts.Problems = append(out.Artifacts.Problems, a.File+": "+strings.Join(a.ShapeErrors, "; "))
		}
	}

	entries, err := session.Today(c.Project)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		out.Session = append(out.Session, sessionLine(e))
	}

	switch {
	case len(out.Runs) > 0 && out.Runs[0].Current != nil:
		out.NextAction = "continue run " + out.Runs[0].ID[:8] + ": step " + itoa(out.Runs[0].Current.Idx) + " " + out.Runs[0].Current.Text
	case len(out.Desk) > 0:
		out.NextAction = "work the desk: " + len2(out.Desk) + " open item(s)"
	case len(out.Cards.Mine) > 0:
		out.NextAction = "continue " + out.Cards.Mine[0].Key + " " + out.Cards.Mine[0].Title + " (card_show first)"
	case len(out.Cards.Ready) > 0:
		out.NextAction = "pick a ready card: " + out.Cards.Ready[0].Key + " " + out.Cards.Ready[0].Title + " (card_show, then card_claim)"
	case len(out.Session) == 0:
		out.NextAction = "log the first session entry of the day"
	default:
		out.NextAction = "pick up work; log as you go"
	}
	return out, nil
}

// ------------------------------------------------------------------ desk

type DeskItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	Text      string `json:"text"`
	Ref       string `json:"ref,omitempty"`
	CreatedAt string `json:"createdAt"`
	State     string `json:"state"`
	ClosedAt  string `json:"closedAt,omitempty"`
	ClosedBy  string `json:"closedBy,omitempty"`
	Answer    string `json:"answer,omitempty"`
	Closer    string `json:"closer,omitempty"` // the verb that closes this type; empty for handoff
}

func deskItem(it desk.Item) DeskItem {
	return DeskItem{ID: it.ID, Type: it.Type, From: it.From, To: it.To, Text: it.Text, Ref: it.Ref,
		CreatedAt: ts(it.CreatedAt), State: it.State, ClosedAt: ts(it.ClosedAt), ClosedBy: it.ClosedBy,
		Answer: it.Answer, Closer: desk.Closer[it.Type]}
}

type DeskListIn struct {
	All  bool   `json:"all,omitempty" jsonschema:"Include closed items."`
	Type string `json:"type,omitempty" jsonschema:"Only this type: info | ask | event | work | handoff."`
}

type DeskListOut struct {
	Agent string     `json:"agent"`
	Items []DeskItem `json:"items"`
}

func DeskList(c Ctx, in DeskListIn) (*DeskListOut, error) {
	items, err := desk.List(c.Project, desk.Filter{Agent: c.agent(), All: in.All, Type: in.Type})
	if err != nil {
		return nil, err
	}
	out := &DeskListOut{Agent: c.agent(), Items: []DeskItem{}}
	for _, it := range items {
		out.Items = append(out.Items, deskItem(it))
	}
	return out, nil
}

type DeskSendIn struct {
	Type string `json:"type" jsonschema:"info (context, closes when read) | ask (expects an answer) | event (a webhook; dismiss or promote) | work (a task; closes when done) | handoff (tried, result, next; never closes)."`
	To   string `json:"to,omitempty" jsonschema:"Agent label to address. Empty means anyone in the tenant."`
	Text string `json:"text" jsonschema:"The item. For handoff: what was tried, the result, what comes next."`
	Ref  string `json:"ref,omitempty" jsonschema:"Optional pointer: a card key, run id, or artifact file."`
}

func DeskSend(c Ctx, in DeskSendIn) (*DeskItem, error) {
	it, err := desk.Send(c.Project, in.Type, c.agent(), in.To, in.Text, in.Ref)
	if err != nil {
		return nil, err
	}
	d := deskItem(*it)
	return &d, nil
}

type DeskIDIn struct {
	ID string `json:"id" jsonschema:"Desk item id or unique prefix."`
}

// DeskRead returns an item; an info item is consumed by reading it.
func DeskRead(c Ctx, in DeskIDIn) (*DeskItem, error) {
	it, err := desk.Read(c.Project, in.ID, c.agent())
	if err != nil {
		return nil, err
	}
	d := deskItem(*it)
	return &d, nil
}

type DeskAnswerIn struct {
	ID     string `json:"id" jsonschema:"Desk item id or unique prefix. Must be an ask."`
	Answer string `json:"answer" jsonschema:"The reply. Required; an ask cannot close without one."`
}

func DeskAnswer(c Ctx, in DeskAnswerIn) (*DeskItem, error) {
	it, err := desk.Answer(c.Project, in.ID, c.agent(), in.Answer)
	if err != nil {
		return nil, err
	}
	d := deskItem(*it)
	return &d, nil
}

func DeskDone(c Ctx, in DeskIDIn) (*DeskItem, error) {
	it, err := desk.Done(c.Project, in.ID, c.agent())
	if err != nil {
		return nil, err
	}
	d := deskItem(*it)
	return &d, nil
}

func DeskDismiss(c Ctx, in DeskIDIn) (*DeskItem, error) {
	it, err := desk.Dismiss(c.Project, in.ID, c.agent())
	if err != nil {
		return nil, err
	}
	d := deskItem(*it)
	return &d, nil
}

// --------------------------------------------------------------- session

type SessionLine struct {
	// ID is the entry's UUID v7, the same on every machine. Short is its last
	// eight hex digits, enough to name it in a runbook step or a message.
	ID    string `json:"id"`
	Short string `json:"short"`
	At    string `json:"at"`
	Agent string `json:"agent"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
}

func sessionLine(e session.Entry) SessionLine {
	return SessionLine{ID: e.ID, Short: e.Short(), At: ts(e.At), Agent: e.Agent, Kind: e.Kind, Text: e.Text}
}

type SessionNewIn struct {
	Kind string `json:"kind" jsonschema:"done (something finished; name files or the card) | decision (a choice; architectural ones also earn an ADR) | problem (what broke and how it was resolved) | next (where the next agent starts)."`
	Text string `json:"text" jsonschema:"One line. Write it as the work happens, not at the end."`
}

func SessionNew(c Ctx, in SessionNewIn) (*SessionLine, error) {
	e, err := session.Append(c.Project, in.Kind, c.agent(), in.Text)
	if err != nil {
		return nil, err
	}
	l := sessionLine(*e)
	return &l, nil
}

type SessionListIn struct {
	Days int `json:"days,omitempty" jsonschema:"How many calendar days back. Default 3."`
}

type SessionListOut struct {
	Entries []SessionLine `json:"entries"`
}

func SessionList(c Ctx, in SessionListIn) (*SessionListOut, error) {
	if in.Days <= 0 {
		in.Days = 3
	}
	entries, err := session.Recent(c.Project, in.Days)
	if err != nil {
		return nil, err
	}
	out := &SessionListOut{Entries: []SessionLine{}}
	for _, e := range entries {
		out.Entries = append(out.Entries, sessionLine(e))
	}
	return out, nil
}

// -------------------------------------------------------------- artifact

type Artifact struct {
	ID          string   `json:"id"`
	Prefix      string   `json:"prefix"`
	Slug        string   `json:"slug"`
	File        string   `json:"file"`
	Path        string   `json:"path"`
	Title       string   `json:"title"`
	Status      string   `json:"status,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	CreatedBy   string   `json:"createdBy"`
	UpdatedAt   string   `json:"updatedAt"`
	FilePresent bool     `json:"filePresent"`
	ShapeOK     bool     `json:"shapeOk"`
	ShapeErrors []string `json:"shapeErrors,omitempty"`
	Tags        []string `json:"tags"`
}

func artifactOut(a artifact.Artifact) Artifact {
	tags := a.Tags
	if tags == nil {
		tags = []string{}
	}
	return Artifact{ID: a.ID, Prefix: a.Prefix, Slug: a.Slug, File: a.File, Path: a.Path, Title: a.Title,
		Status: a.Status, CreatedAt: ts(a.CreatedAt), CreatedBy: a.CreatedBy, UpdatedAt: ts(a.UpdatedAt),
		FilePresent: a.FilePresent, ShapeOK: a.ShapeOK, ShapeErrors: a.ShapeErrors, Tags: tags}
}

type ArtifactNewIn struct {
	Prefix string `json:"prefix" jsonschema:"adr (a decision; numbered, never rewritten) | story (a feature in the user's words) | idea | index | free. Runbooks are authored with runbook_new."`
	Title  string `json:"title" jsonschema:"Human title. The file slug is derived from it."`
}

// ArtifactNew creates the row, then the file seeded from the shape. The new
// file fails its shape until the sections are filled; that is expected.
func ArtifactNew(c Ctx, in ArtifactNewIn) (*Artifact, error) {
	a, err := artifact.New(c.Project, in.Prefix, in.Title, c.agent())
	if err != nil {
		return nil, err
	}
	o := artifactOut(*a)
	return &o, nil
}

type ArtifactListIn struct {
	Tag string `json:"tag,omitempty" jsonschema:"Only artifacts carrying this tag."`
}

type ArtifactListOut struct {
	Artifacts []Artifact `json:"artifacts"`
}

func ArtifactList(c Ctx, in ArtifactListIn) (*ArtifactListOut, error) {
	list, err := artifact.List(c.Project)
	if err != nil {
		return nil, err
	}
	out := &ArtifactListOut{Artifacts: []Artifact{}}
	want := project.Slugify(in.Tag)
	for _, a := range list {
		if want != "" && !contains(a.Tags, want) {
			continue
		}
		out.Artifacts = append(out.Artifacts, artifactOut(a))
	}
	return out, nil
}

type ArtifactTagIn struct {
	Artifact string   `json:"artifact" jsonschema:"File name or id (or id prefix)."`
	Add      []string `json:"add,omitempty" jsonschema:"Tags to add."`
	Remove   []string `json:"remove,omitempty" jsonschema:"Tags to remove."`
}

func ArtifactTag(c Ctx, in ArtifactTagIn) (*Artifact, error) {
	var changes []string
	for _, t := range in.Add {
		changes = append(changes, "+"+t)
	}
	for _, t := range in.Remove {
		changes = append(changes, "-"+t)
	}
	a, err := artifact.Tag(c.Project, in.Artifact, changes)
	if err != nil {
		return nil, err
	}
	o := artifactOut(*a)
	return &o, nil
}

type ArtifactDeleteIn struct {
	Artifact string `json:"artifact" jsonschema:"File name, id, or id prefix (the 8-char short id works)."`
}

// ArtifactDelete removes the row and the file together and records the
// delete for the next sync, which soft-deletes it on the server.
func ArtifactDelete(c Ctx, in ArtifactDeleteIn) (*Artifact, error) {
	a, err := artifact.Delete(c.Project, in.Artifact)
	if err != nil {
		return nil, err
	}
	o := artifactOut(*a)
	return &o, nil
}

type ArtifactReconcileIn struct{}

type ReconcileChange struct {
	Action string `json:"action"`
	File   string `json:"file"`
	Detail string `json:"detail"`
}

type ArtifactReconcileOut struct {
	Changes []ReconcileChange `json:"changes"`
}

func ArtifactReconcile(c Ctx, _ ArtifactReconcileIn) (*ArtifactReconcileOut, error) {
	changes, err := artifact.Reconcile(c.Project)
	if err != nil {
		return nil, err
	}
	out := &ArtifactReconcileOut{Changes: []ReconcileChange{}}
	for _, ch := range changes {
		out.Changes = append(out.Changes, ReconcileChange{ch.Action, ch.File, ch.Detail})
	}
	return out, nil
}

type ArtifactValidateIn struct {
	File string `json:"file,omitempty" jsonschema:"One file to check. Empty checks all."`
}

type ArtifactProblem struct {
	File   string   `json:"file"`
	Errors []string `json:"errors"`
}

type ArtifactValidateOut struct {
	Checked  int               `json:"checked"`
	Problems []ArtifactProblem `json:"problems"`
}

func ArtifactValidate(c Ctx, in ArtifactValidateIn) (*ArtifactValidateOut, error) {
	list, err := artifact.List(c.Project)
	if err != nil {
		return nil, err
	}
	out := &ArtifactValidateOut{Problems: []ArtifactProblem{}}
	for _, a := range list {
		if in.File != "" && a.File != in.File {
			continue
		}
		out.Checked++
		if errs := artifact.Validate(c.Project, a); len(errs) > 0 {
			out.Problems = append(out.Problems, ArtifactProblem{a.File, errs})
		}
	}
	return out, nil
}

type ShapesIn struct{}

type Shape struct {
	Prefix      string   `json:"prefix"`
	Title       string   `json:"title"`
	FrontMatter []string `json:"frontMatter"`
	Sections    []string `json:"sections"`
	MinBody     int      `json:"minBody"`
	Source      string   `json:"source"` // built-in or override file
}

type ShapesOut struct {
	Shapes []Shape `json:"shapes"`
}

func Shapes(c Ctx, _ ShapesIn) (*ShapesOut, error) {
	set, err := shape.Load(c.Project.ArtifactsDir())
	if err != nil {
		return nil, err
	}
	out := &ShapesOut{Shapes: []Shape{}}
	for _, pre := range artifact.Prefixes {
		s, ok := set[pre]
		if !ok {
			continue
		}
		src := s.Source
		if src == "" {
			src = "built-in"
		}
		out.Shapes = append(out.Shapes, Shape{pre, s.Title, nz(s.FrontMatter), nz(s.Sections), s.MinBody, src})
	}
	return out, nil
}

// --------------------------------------------------------------- runbook

type RunbookStep struct {
	Idx   int    `json:"idx"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Check string `json:"check,omitempty"` // check:arg, e.g. card:Testing
	Name  string `json:"name,omitempty"`
	When  string `json:"when,omitempty"`
}

type Runbook struct {
	Slug   string        `json:"slug"`
	Title  string        `json:"title"`
	Target string        `json:"target"` // card | repo | none
	Path   string        `json:"path"`
	Steps  []RunbookStep `json:"steps"`
}

func runbookOut(rb *runbook.Runbook) Runbook {
	out := Runbook{Slug: rb.Slug, Title: rb.Title, Target: rb.Target, Path: rb.Path, Steps: []RunbookStep{}}
	for i, s := range rb.Steps {
		label := s.Check
		if s.Arg != "" {
			label += ":" + s.Arg
		}
		out.Steps = append(out.Steps, RunbookStep{i + 1, s.Kind, s.Text, label, s.Name, s.When})
	}
	return out
}

type RunStep struct {
	Idx       int    `json:"idx"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Check     string `json:"check,omitempty"`
	Name      string `json:"name,omitempty"`
	When      string `json:"when,omitempty"`
	State     string `json:"state"` // pending | current | done | verified | skipped
	Evidence  string `json:"evidence,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	DoneAt    string `json:"doneAt,omitempty"`
	Reason    string `json:"reason,omitempty"`
	DeskItem  string `json:"deskItem,omitempty"`
}

type Run struct {
	ID        string    `json:"id"`
	Runbook   string    `json:"runbook"`
	Target    string    `json:"target,omitempty"`
	Card      string    `json:"card,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Agent     string    `json:"agent"`
	StartedAt string    `json:"startedAt"`
	EndedAt   string    `json:"endedAt,omitempty"`
	Status    string    `json:"status"`
	Steps     []RunStep `json:"steps"`
	Current   *RunStep  `json:"current,omitempty"`
}

type RunSummary struct {
	ID      string   `json:"id"`
	Runbook string   `json:"runbook"`
	Target  string   `json:"target,omitempty"`
	Card    string   `json:"card,omitempty"`
	Branch  string   `json:"branch,omitempty"`
	Agent   string   `json:"agent"`
	Done    int      `json:"done"`
	Total   int      `json:"total"`
	Current *RunStep `json:"current,omitempty"`
}

func runStep(s runbook.RunStep) RunStep {
	return RunStep{Idx: s.Idx, Kind: s.Kind, Text: s.Text, Check: s.Label(), Name: s.Name, When: s.When, State: s.State,
		Evidence: s.Evidence, StartedAt: ts(s.StartedAt), DoneAt: ts(s.DoneAt), Reason: s.Reason, DeskItem: s.DeskItem}
}

func runOut(r *runbook.Run) Run {
	out := Run{ID: r.ID, Runbook: r.Runbook, Target: r.Target, Card: r.CardKey, Branch: r.Branch, Agent: r.Agent, StartedAt: ts(r.StartedAt),
		EndedAt: ts(r.EndedAt), Status: r.Status, Steps: []RunStep{}}
	for _, s := range r.Steps {
		out.Steps = append(out.Steps, runStep(s))
	}
	if cur := r.Current(); cur != nil {
		c := runStep(*cur)
		out.Current = &c
	}
	return out
}

func runSummary(r runbook.Run) RunSummary {
	out := RunSummary{ID: r.ID, Runbook: r.Runbook, Target: r.Target, Card: r.CardKey, Branch: r.Branch, Agent: r.Agent, Total: len(r.Steps)}
	for _, s := range r.Steps {
		if s.State != "pending" && s.State != "current" {
			out.Done++
		}
	}
	if cur := r.Current(); cur != nil {
		c := runStep(*cur)
		out.Current = &c
	}
	return out
}

type RunbookNewIn struct {
	Title  string `json:"title" jsonschema:"Name of the process, e.g. Single ticket development."`
	Target string `json:"target,omitempty" jsonschema:"card (a run needs a card and owns its state) | repo (about the repository) | none (default)."`
}

func RunbookNew(c Ctx, in RunbookNewIn) (*Runbook, error) {
	rb, err := runbook.New(c.Project, in.Title, in.Target)
	if err != nil {
		return nil, err
	}
	o := runbookOut(rb)
	return &o, nil
}

type RunbookStepAddIn struct {
	Runbook string `json:"runbook" jsonschema:"Runbook slug."`
	Kind    string `json:"kind" jsonschema:"ask (prompt the user; the answer is the evidence) | do (agent work) | gears (a Gears verb performed as the step) | git (a repo state) | gate (wait until a check holds)."`
	Text    string `json:"text" jsonschema:"What the step asks for, in one line."`
	Check   string `json:"check,omitempty" jsonschema:"How the step is verified against the run, as check or check:arg. do: commit | clean. gears: session (logs the evidence text) | desk | artifact | card:<State> (moves the run's card). git: branch (binds the run) | commit (on the run's branch, after start) | pushed | merged:<base> | clean. gate: pushed | merged:<base> | card:<State> | desk (closed). ask takes none."`
	Name    string `json:"name,omitempty" jsonschema:"Slug so a later step's when can refer to this step's evidence."`
	When    string `json:"when,omitempty" jsonschema:"Condition on an earlier named step, written name=value or name!=value. The step is skipped, visibly, when it does not hold."`
}

func RunbookStepAdd(c Ctx, in RunbookStepAddIn) (*Runbook, error) {
	rb, err := runbook.Load(c.Project, in.Runbook)
	if err != nil {
		return nil, err
	}
	if err := rb.AddStep(in.Kind, in.Text, in.Check, "", in.Name, in.When); err != nil {
		return nil, err
	}
	o := runbookOut(rb)
	return &o, nil
}

type RunbookStepUpdateIn struct {
	Runbook string  `json:"runbook" jsonschema:"Runbook slug."`
	Step    int     `json:"step" jsonschema:"Step number, 1-based (see runbook_show)."`
	Kind    *string `json:"kind,omitempty" jsonschema:"New kind: ask | do | gears | git | gate. Omit to keep."`
	Text    *string `json:"text,omitempty" jsonschema:"New text. Omit to keep."`
	Check   *string `json:"check,omitempty" jsonschema:"New check or check:arg; empty string clears it. Omit to keep."`
	Name    *string `json:"name,omitempty" jsonschema:"New name; empty string clears it. Refused while a later when: refers to the current name. Omit to keep."`
	When    *string `json:"when,omitempty" jsonschema:"New condition name=value or name!=value on an earlier step; empty string clears it. Omit to keep."`
}

// RunbookStepUpdate edits one step in place and revalidates the runbook.
func RunbookStepUpdate(c Ctx, in RunbookStepUpdateIn) (*Runbook, error) {
	rb, err := runbook.Load(c.Project, in.Runbook)
	if err != nil {
		return nil, err
	}
	if err := rb.UpdateStep(c.Project, in.Step, runbook.StepPatch{Kind: in.Kind, Text: in.Text, Check: in.Check, Name: in.Name, When: in.When}); err != nil {
		return nil, err
	}
	o := runbookOut(rb)
	return &o, nil
}

type RunbookStepRemoveIn struct {
	Runbook string `json:"runbook" jsonschema:"Runbook slug."`
	Step    int    `json:"step" jsonschema:"Step number, 1-based."`
}

// RunbookStepRemove deletes a step; later steps renumber.
func RunbookStepRemove(c Ctx, in RunbookStepRemoveIn) (*Runbook, error) {
	rb, err := runbook.Load(c.Project, in.Runbook)
	if err != nil {
		return nil, err
	}
	if err := rb.RemoveStep(c.Project, in.Step); err != nil {
		return nil, err
	}
	o := runbookOut(rb)
	return &o, nil
}

type RunbookStepMoveIn struct {
	Runbook string `json:"runbook" jsonschema:"Runbook slug."`
	Step    int    `json:"step" jsonschema:"Step number to move, 1-based."`
	To      int    `json:"to" jsonschema:"Position it lands on, 1-based."`
}

// RunbookStepMove reorders; refused when a when: would precede its step.
func RunbookStepMove(c Ctx, in RunbookStepMoveIn) (*Runbook, error) {
	rb, err := runbook.Load(c.Project, in.Runbook)
	if err != nil {
		return nil, err
	}
	if err := rb.MoveStep(c.Project, in.Step, in.To); err != nil {
		return nil, err
	}
	o := runbookOut(rb)
	return &o, nil
}

type RunbookUpdateIn struct {
	Runbook string `json:"runbook" jsonschema:"Runbook slug. The slug and file name never change."`
	Title   string `json:"title,omitempty" jsonschema:"New title. Omit to keep."`
	Target  string `json:"target,omitempty" jsonschema:"New target: card | repo | none. Refused if a step's check needs a card. Omit to keep."`
}

// RunbookUpdate changes title or target.
func RunbookUpdate(c Ctx, in RunbookUpdateIn) (*Runbook, error) {
	rb, err := runbook.Load(c.Project, in.Runbook)
	if err != nil {
		return nil, err
	}
	if err := rb.Update(c.Project, in.Title, in.Target); err != nil {
		return nil, err
	}
	o := runbookOut(rb)
	return &o, nil
}

type RunbookDeleteIn struct {
	Runbook string `json:"runbook" jsonschema:"Runbook slug."`
}

type RunbookDeleteOut struct {
	Slug    string `json:"slug"`
	Deleted bool   `json:"deleted"`
}

// RunbookDelete removes the definition file. Past runs keep their copy.
func RunbookDelete(c Ctx, in RunbookDeleteIn) (*RunbookDeleteOut, error) {
	if err := runbook.Delete(c.Project, in.Runbook); err != nil {
		return nil, err
	}
	return &RunbookDeleteOut{Slug: in.Runbook, Deleted: true}, nil
}

type RunbookCopyIn struct {
	Runbook string `json:"runbook" jsonschema:"Slug to copy. With no from, one of the built-ins listed by runbook_list."`
	From    string `json:"from,omitempty" jsonschema:"Another project's root directory (its .gears/artifacts/runbook--<slug>.yaml is read). Omit for a built-in."`
	As      string `json:"as,omitempty" jsonschema:"Title for the copy; its slug is derived. Omit to keep the source slug."`
}

// RunbookCopy brings a built-in or another project's runbook into this one.
func RunbookCopy(c Ctx, in RunbookCopyIn) (*Runbook, error) {
	rb, err := runbook.Copy(c.Project, in.Runbook, in.From, in.As)
	if err != nil {
		return nil, err
	}
	o := runbookOut(rb)
	return &o, nil
}

type RunbookListIn struct{}

type RunbookListOut struct {
	Runbooks   []Runbook           `json:"runbooks"`
	Active     []RunSummary        `json:"activeRuns"`
	Builtins   []string            `json:"builtins"`   // shipped runbooks a project may copy
	Vocabulary map[string][]string `json:"vocabulary"` // kind -> checks
	Rules      []string            `json:"rules"`
}

func RunbookList(c Ctx, _ RunbookListIn) (*RunbookListOut, error) {
	books, err := runbook.All(c.Project)
	if err != nil {
		return nil, err
	}
	out := &RunbookListOut{Runbooks: []Runbook{}, Active: []RunSummary{}, Builtins: runbook.BuiltinSlugs(), Vocabulary: runbook.Checks, Rules: []string{
		"One active run per agent.",
		"Only the agent that started a run advances it; runbook_takeover with a reason moves it.",
		"A run on a card owns the card's state: card_move is refused, gears card:<State> steps move it.",
		"Evidence is a Gears or git object checked against the run (its card, its branch, when the step began). No shell commands.",
		"A runbook with an active run is frozen: step edits, update and delete wait until the run ends.",
	}}
	for _, rb := range books {
		out.Runbooks = append(out.Runbooks, runbookOut(rb))
	}
	runs, err := runbook.Active(c.Project)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		out.Active = append(out.Active, runSummary(r))
	}
	return out, nil
}

type RunbookShowIn struct {
	Key string `json:"key" jsonschema:"A runbook slug, or a run id (or prefix)."`
}

type RunbookShowOut struct {
	Runbook *Runbook `json:"runbook,omitempty"`
	Run     *Run     `json:"run,omitempty"`
}

func RunbookShow(c Ctx, in RunbookShowIn) (*RunbookShowOut, error) {
	if r, err := runbook.Get(c.Project, in.Key); err == nil {
		o := runOut(r)
		return &RunbookShowOut{Run: &o}, nil
	}
	rb, err := runbook.Load(c.Project, in.Key)
	if err != nil {
		return nil, err
	}
	o := runbookOut(rb)
	return &RunbookShowOut{Runbook: &o}, nil
}

type RunbookStartIn struct {
	Runbook string `json:"runbook" jsonschema:"Runbook slug."`
	Target  string `json:"target,omitempty" jsonschema:"For a card runbook: the card key. The card is assigned to you and owned by the run until it ends."`
}

// RunbookStart creates a run and places step 1 on the agent's desk.
func RunbookStart(c Ctx, in RunbookStartIn) (*Run, error) {
	r, err := runbook.Start(c.Project, in.Runbook, in.Target, c.agent())
	if err != nil {
		return nil, err
	}
	o := runOut(r)
	return &o, nil
}

type RunRef struct {
	Run string `json:"run,omitempty" jsonschema:"Run id or prefix. Omit for your own active run."`
}

func (c Ctx) resolveRun(key string) (*runbook.Run, error) {
	if key != "" {
		return runbook.Get(c.Project, key)
	}
	return runbook.Only(c.Project, c.agent())
}

type RunStepDoneIn struct {
	RunRef
	Evidence string `json:"evidence,omitempty" jsonschema:"What the step's check asks for. branch: name (default: current). commit: sha (default: HEAD). session: the text to log, or an entry id (full, or its 8-digit short). desk: item id. artifact: file. ask or no check: the answer or a note. gears card:<State>, gate, pushed, merged, clean: none needed."`
}

type RunTakeoverIn struct {
	RunRef
	Reason string `json:"reason" jsonschema:"Why you are taking this run from its agent. Required; logged to the session and the card."`
}

// RunTakeover moves another agent's active run to this agent.
func RunTakeover(c Ctx, in RunTakeoverIn) (*Run, error) {
	if in.Run == "" {
		return nil, fmt.Errorf("name the run to take over")
	}
	r, err := runbook.Takeover(c.Project, in.Run, c.agent(), in.Reason)
	if err != nil {
		return nil, err
	}
	o := runOut(r)
	return &o, nil
}

type RunStepOut struct {
	Step     RunStep  `json:"step"`
	Verified bool     `json:"verified"`
	Note     string   `json:"note"`
	Finished bool     `json:"runFinished"`
	Next     *RunStep `json:"next,omitempty"`
	RunID    string   `json:"runId"`
}

// RunStepDone verifies evidence for the current step and advances. A failed
// check is an error and the step stays current.
func RunStepDone(c Ctx, in RunStepDoneIn) (*RunStepOut, error) {
	r, err := c.resolveRun(in.Run)
	if err != nil {
		return nil, err
	}
	o, err := runbook.Complete(c.Project, r.ID, in.Evidence, c.agent())
	if err != nil {
		return nil, err
	}
	return stepOut(c, r.ID, o), nil
}

type RunSkipIn struct {
	RunRef
	Reason string `json:"reason" jsonschema:"Why the step is skipped. Required; it stays visible on the run."`
}

func RunSkip(c Ctx, in RunSkipIn) (*RunStepOut, error) {
	r, err := c.resolveRun(in.Run)
	if err != nil {
		return nil, err
	}
	o, err := runbook.Skip(c.Project, r.ID, in.Reason, c.agent())
	if err != nil {
		return nil, err
	}
	return stepOut(c, r.ID, o), nil
}

func stepOut(c Ctx, runID string, o *runbook.Outcome) *RunStepOut {
	out := &RunStepOut{Step: runStep(o.Step), Verified: o.Verified, Note: o.Note, Finished: o.Finished, RunID: runID}
	if !o.Finished {
		if r, err := runbook.Get(c.Project, runID); err == nil {
			if cur := r.Current(); cur != nil {
				n := runStep(*cur)
				out.Next = &n
			}
		}
	}
	return out
}

type RunAbandonIn struct {
	RunRef
	Reason string `json:"reason" jsonschema:"Why. Required; logged to the session as a problem."`
}

func RunAbandon(c Ctx, in RunAbandonIn) (*Run, error) {
	r, err := c.resolveRun(in.Run)
	if err != nil {
		return nil, err
	}
	r, err = runbook.Abandon(c.Project, r.ID, in.Reason, c.agent())
	if err != nil {
		return nil, err
	}
	o := runOut(r)
	return &o, nil
}

// --------------------------------------------------------------- helpers

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func itoa(n int) string { return strconv.Itoa(n) }

func len2(items []DeskItem) string { return strconv.Itoa(len(items)) }
