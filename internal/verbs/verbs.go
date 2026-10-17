// Package verbs is the one contract both doors share. Every operation an
// agent can perform is a function here with a typed input and a typed,
// JSON-shaped output. The MCP server registers these as tools; the CLI
// renders them as text or --json. Rules live below this layer, in the
// domain packages; this layer names them once and documents them once, so
// the two doors cannot drift.
package verbs

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/card"
	"github.com/Syluxso/gears-cli/internal/desk"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/runbook"
	"github.com/Syluxso/gears-cli/internal/runs"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/shape"
)

// Ctx is what every verb needs: the bound project and the acting agent.
// Current is the project the command runs in (the manifest entry whose
// path holds the working directory), nil at the workspace root; session
// entries and new artifacts are tagged with it. Desk stays workspace-level.
type Ctx struct {
	Project *project.Project
	Agent   string
	// AgentSource is where Agent came from: session.AgentFrom* (GRS-21).
	AgentSource string
	Current     *project.ManifestEntry
	// Notes receives one-line notes a verb has for a person (the CLI sets
	// stderr): a card write that could not reach gears-api, for one. The
	// MCP server leaves it nil; its results carry the note.
	Notes io.Writer
}

// projectFor resolves the project a write is about: an explicit key (id,
// slug or path from the manifest), else the current project, else none.
func (c Ctx) projectFor(key string) (string, error) {
	if key == "" {
		if c.Current != nil {
			return c.Current.ID, nil
		}
		return "", nil
	}
	if !c.Project.IsWorkspace() {
		if key == c.Project.ID || key == c.Project.Slug {
			return c.Project.ID, nil
		}
		return "", fmt.Errorf("project %q: this binding is project %s only", key, c.Project.Slug)
	}
	m, err := project.LoadManifest(c.Project)
	if err != nil {
		return "", err
	}
	if e := m.Lookup(key); e != nil {
		return e.ID, nil
	}
	return "", fmt.Errorf("no project %q in %s; run gears projects list", key, c.Project.ManifestPath())
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

// StatusOut is the cold-start packet: the whole workspace (GRS-46), not
// the one project the command runs in.
type StatusOut struct {
	// Project and ProjectID are the binding at Root: the workspace itself
	// at a workspace root, the project for a standalone clone.
	Project   string `json:"project"`
	ProjectID string `json:"projectId"`
	Root      string `json:"root"`
	// IsWorkspace is true at a workspace root, false for a standalone clone.
	IsWorkspace bool `json:"isWorkspace"`
	// Projects is every project in the workspace: manifest, disk and API.
	Projects []StatusProject `json:"projects"`
	API      StatusAPI       `json:"api"`
	// Current is the project the working directory is in (GRS-40); absent
	// at the workspace root.
	Current    *project.ManifestEntry `json:"current,omitempty"`
	Agent      AgentInfo              `json:"agent"`
	Prefix     string                 `json:"prefix"`
	Desk       []DeskItem             `json:"desk"`
	Cards      CardStats              `json:"cards"`
	Runs       []RunSummary           `json:"runs"`   // running runs of the workspace, mine first
	MyRuns     []RunSummary           `json:"myRuns"` // GRS-71: my running runs, with their current step
	Artifacts  ArtifactStats          `json:"artifacts"`
	Session    []SessionLine          `json:"sessionToday"`
	NextAction string                 `json:"nextAction"`
	// Arch is the workspace's architecture artifact in full (GRS-41),
	// always carried so an agent never has to go looking for it.
	Arch ArchOut `json:"arch"`
	// Presence is who holds the workspace's board stream right now
	// (GRS-37): browsers, gears run (with its auto-sync interval). Absent
	// when the API cannot say (offline, not logged in, before presence).
	Presence *api.Presence `json:"presence,omitempty"`
}

// ArchOut is the workspace's master architecture doc: arch--primary.md,
// free-form, one per workspace. Missing (no body) says how to create it.
type ArchOut struct {
	File    string `json:"file"`
	Body    string `json:"body"`
	Missing bool   `json:"missing,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// archMissingHint is how to create the arch artifact in a workspace that
// predates it. Status only says so; it never writes the file.
const archMissingHint = "no architecture artifact yet: gears arch init (or re-run gears init at the workspace root) writes a stub " + artifact.ArchFile

func archOut(c Ctx) (ArchOut, error) {
	_, body, ok, err := artifact.Arch(c.Project)
	if err != nil {
		return ArchOut{}, err
	}
	if !ok {
		return ArchOut{File: artifact.ArchFile, Missing: true, Hint: archMissingHint}, nil
	}
	return ArchOut{File: artifact.ArchFile, Body: body}, nil
}

// ArchIn has no fields: there is one architecture artifact per workspace.
type ArchIn struct{}

// Arch returns the workspace's architecture artifact in full.
func Arch(c Ctx, _ ArchIn) (*ArchOut, error) {
	if _, err := artifact.Reconcile(c.Project); err != nil {
		return nil, err
	}
	o, err := archOut(c)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

type ArchInitIn struct{}

type ArchInitOut struct {
	File    string `json:"file"`
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

// ArchInit writes the stub arch--primary.md if the workspace has none. It
// never overwrites one that exists.
func ArchInit(c Ctx, _ ArchInitIn) (*ArchInitOut, error) {
	a, created, err := artifact.InitArch(c.Project, c.agent())
	if err != nil {
		return nil, err
	}
	return &ArchInitOut{File: a.File, Path: a.Path, Created: created}, nil
}

// CardStats is the board at a glance: open cards by state, in board order,
// and the ready cards (default state) an agent can pick up.
type CardStats struct {
	// Stale: the API did not answer the cards pull in time (GRS-51).
	Stale   bool        `json:"stale,omitempty"`
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
	if err := c.CheckLive(); err != nil { // GRS-80
		return nil, err
	}
	out := &StatusOut{Project: c.Project.Slug, ProjectID: c.Project.ID, Root: c.Project.Root, Agent: c.agentInfo(), Prefix: c.Project.Prefix,
		IsWorkspace: c.Project.IsWorkspace(), Desk: []DeskItem{}, Runs: []RunSummary{}, MyRuns: []RunSummary{}, Session: []SessionLine{},
		Cards:     CardStats{ByState: []CardState{}, Mine: []CardLine{}, Ready: []CardLine{}},
		Artifacts: ArtifactStats{ByPrefix: map[string]int{}, Problems: []string{}}, Current: c.Current}

	// Git and the API are the slow part; they run while the local record
	// is read.
	type wsResult struct {
		projects []StatusProject
		api      StatusAPI
		err      error
	}
	ws := make(chan wsResult, 1)
	go func() {
		p, a, err := workspaceProjects(c)
		ws <- wsResult{p, a, err}
	}()
	defer func() { <-ws }() // never leave it running past an early return
	// The cards section reads after a backstop pull (GRS-51), which runs
	// alongside the desk, runs and git work.
	down := make(chan bool, 1) // buffered: an early return never strands it
	go func() { down <- c.cardsDown() }()
	pres := make(chan *api.Presence, 1)
	go func() { pres <- apiPresence(c.Project.WorkspaceID()) }()

	items, err := desk.List(c.Project, mine(c, out.Agent.Label))
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		out.Desk = append(out.Desk, deskItem(it))
	}

	// GRS-58: the running runs of the gears record, as last synced.
	recs, err := runs.List(c.Project, runs.Filter{Status: "running"})
	if err != nil {
		return nil, err
	}
	var others []RunSummary
	for _, r := range recs {
		rs := recordSummary(r)
		if rs.Agent == out.Agent.Label {
			rs.Mine = true
			out.MyRuns = append(out.MyRuns, rs)
		} else {
			others = append(others, rs)
		}
	}
	out.Runs = append(append(out.Runs, out.MyRuns...), others...)

	out.Cards.Stale = <-down
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
			if cd.Assignee == out.Agent.Label {
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
		case a.Prefix == "arch" && a.File != artifact.ArchFile:
			// A hand-made or pulled second arch file: only the primary is shown.
			out.Artifacts.Problems = append(out.Artifacts.Problems, a.File+": one arch per workspace; only "+artifact.ArchFile+" is shown by status")
		case !a.FilePresent:
			out.Artifacts.Problems = append(out.Artifacts.Problems, a.File+": file missing")
		case !a.ShapeOK:
			out.Artifacts.Problems = append(out.Artifacts.Problems, a.File+": "+strings.Join(a.ShapeErrors, "; "))
		}
	}
	if out.Arch, err = archOut(c); err != nil {
		return nil, err
	}

	entries, err := session.Today(c.Project)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		out.Session = append(out.Session, sessionLine(e))
	}

	r := <-ws
	ws <- r // for the deferred receive
	if r.err != nil {
		return nil, r.err
	}
	out.Projects, out.API = r.projects, r.api
	out.Presence = <-pres

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
	// GRS-35: the user who created it (set by the API; "" until synced) and
	// the user it is for ("" nobody in particular).
	Author     string `json:"author"`
	AssignedTo string `json:"assignedTo,omitempty"`
}

func deskItem(it desk.Item) DeskItem {
	return DeskItem{ID: it.ID, Type: it.Type, From: it.From, To: it.To, Text: it.Text, Ref: it.Ref,
		CreatedAt: ts(it.CreatedAt), State: it.State, ClosedAt: ts(it.ClosedAt), ClosedBy: it.ClosedBy,
		Answer: it.Answer, Closer: desk.Closer[it.Type], Author: it.Author, AssignedTo: it.AssignedTo}
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
	f := mine(c, c.agent())
	f.All, f.Type = in.All, in.Type
	items, err := desk.List(c.Project, f)
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
	Type       string `json:"type" jsonschema:"info (context, closes when read) | ask (expects an answer) | event (a webhook; dismiss or promote) | work (a task; closes when done) | handoff (tried, result, next; never closes)."`
	To         string `json:"to,omitempty" jsonschema:"Agent label to address. Empty means anyone in the tenant."`
	Text       string `json:"text" jsonschema:"The item. For handoff: what was tried, the result, what comes next."`
	Ref        string `json:"ref,omitempty" jsonschema:"Optional pointer: a card key, run id, or artifact file."`
	AssignedTo string `json:"assignedTo,omitempty" jsonschema:"The person the item is for: an id from people_list (call it first; never guess one). With to empty the item is for that person and any of their agents."`
}

func DeskSend(c Ctx, in DeskSendIn) (*DeskItem, error) {
	if err := checkAssignee(c, in.AssignedTo); err != nil {
		return nil, err
	}
	it, err := desk.SendAssigned(c.Project, in.Type, c.agent(), in.To, in.Text, in.Ref, in.AssignedTo)
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
	// GRS-88: a run's approval or gate item is a person's decision, made in
	// the Gears app (run page or desk), never an agent's answer.
	if it, err := desk.Get(c.Project, in.ID); err == nil && it.Type == "ask" && strings.HasPrefix(it.Ref, "run-") &&
		(strings.Contains(it.Text, "(approval)") || strings.Contains(it.Text, "(gate)")) {
		return nil, fmt.Errorf("%s is a run's approval step: it needs a person; they approve it in the Gears app (run page or desk), not here", it.Short())
	}
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

// DeskClearIn picks what desk_clear closes.
type DeskClearIn struct {
	All bool `json:"all,omitempty" jsonschema:"close every open item in the workspace; default only items addressed to you"`
}

// DeskClearOut lists what was closed.
type DeskClearOut struct {
	Closed []DeskItem `json:"closed"`
}

func DeskClear(c Ctx, in DeskClearIn) (*DeskClearOut, error) {
	items, err := desk.Clear(c.Project, c.agent(), in.All)
	out := &DeskClearOut{Closed: []DeskItem{}}
	for _, it := range items {
		out.Closed = append(out.Closed, deskItem(it))
	}
	return out, err
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
	// ProjectID is the project the entry is about; absent for workspace-level.
	ProjectID string `json:"projectId,omitempty"`
}

func sessionLine(e session.Entry) SessionLine {
	return SessionLine{ID: e.ID, Short: e.Short(), At: ts(e.At), Agent: e.Agent, Kind: e.Kind, Text: e.Text, ProjectID: e.ProjectID}
}

type SessionNewIn struct {
	Kind    string `json:"kind" jsonschema:"done (something finished; name files or the card) | decision (a choice; architectural ones also earn an ADR) | problem (what broke and how it was resolved) | next (where the next agent starts)."`
	Text    string `json:"text" jsonschema:"One line. Write it as the work happens, not at the end."`
	Project string `json:"project,omitempty" jsonschema:"The project this entry is about: slug, id or path from projects_list. Default: the project the server runs in, else none (workspace-level)."`
}

func SessionNew(c Ctx, in SessionNewIn) (*SessionLine, error) {
	pid, err := c.projectFor(in.Project)
	if err != nil {
		return nil, err
	}
	e, err := session.AppendFor(c.Project, pid, in.Kind, c.agent(), in.Text)
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
	ProjectID   string   `json:"projectId,omitempty"`
}

func artifactOut(a artifact.Artifact) Artifact {
	tags := a.Tags
	if tags == nil {
		tags = []string{}
	}
	return Artifact{ID: a.ID, Prefix: a.Prefix, Slug: a.Slug, File: a.File, Path: a.Path, Title: a.Title,
		Status: a.Status, CreatedAt: ts(a.CreatedAt), CreatedBy: a.CreatedBy, UpdatedAt: ts(a.UpdatedAt),
		FilePresent: a.FilePresent, ShapeOK: a.ShapeOK, ShapeErrors: a.ShapeErrors, Tags: tags, ProjectID: a.ProjectID}
}

type ArtifactNewIn struct {
	Prefix  string `json:"prefix" jsonschema:"adr (a decision; numbered, never rewritten) | story (a feature in the user's words) | idea | index | free | arch (the workspace's one architecture doc, always arch--primary.md; a second is refused). Runbooks are authored with runbook_new."`
	Title   string `json:"title" jsonschema:"Human title. The file slug is derived from it; file names are unique across the workspace."`
	Project string `json:"project,omitempty" jsonschema:"The project this artifact belongs to: slug, id or path from projects_list. Default: the project the server runs in, else none (workspace-level)."`
}

// ArtifactNew creates the row, then the file seeded from the shape. The new
// file fails its shape until the sections are filled; that is expected.
func ArtifactNew(c Ctx, in ArtifactNewIn) (*Artifact, error) {
	pid, err := c.projectFor(in.Project)
	if err != nil {
		return nil, err
	}
	a, err := artifact.NewFor(c.Project, pid, in.Prefix, in.Title, c.agent())
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
	Mine    bool     `json:"mine,omitempty"` // started by the agent status runs as
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
	if err := runs.RefuseAdopted(c.Project, in.Runbook); err != nil {
		return nil, err
	}
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
	if err := runs.RefuseAdopted(c.Project, in.Runbook); err != nil {
		return nil, err
	}
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
	if err := runs.RefuseAdopted(c.Project, in.Runbook); err != nil {
		return nil, err
	}
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
	if err := runs.RefuseAdopted(c.Project, in.Runbook); err != nil {
		return nil, err
	}
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
	if err := runs.RefuseAdopted(c.Project, in.Runbook); err != nil {
		return nil, err
	}
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
	if err := runs.RefuseAdopted(c.Project, in.Runbook); err != nil {
		return nil, err
	}
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

// RunbookListOut is the remote MCP's runbook_list (official, adopted,
// officialAvailable, note) plus what only a local server has: the local
// drafts, the built-ins they may copy, and their vocabulary.
type RunbookListOut struct {
	Official          []api.OfficialRunbook `json:"official"`
	Adopted           []api.AdoptedRunbook  `json:"adopted"`
	OfficialAvailable bool                  `json:"officialAvailable"`
	Note              string                `json:"note,omitempty"`
	Stale             bool                  `json:"stale,omitempty"` // adopted is the local cache: gears-api did not answer
	Drafts            []Runbook             `json:"drafts"`          // local runbook--<slug>.yaml, not runnable as they stand
	Builtins          []string              `json:"builtins"`        // shipped drafts a project may copy
	Vocabulary        map[string][]string   `json:"vocabulary"`      // the drafts' kind -> checks
	Rules             []string              `json:"rules"`
}

// RunbookList is the official catalog and the adopted copies from
// gears-api (GRS-71: the adopted copies are the record and answer without
// the orchestrator; a missing catalog is a note, not an error), then the
// local drafts. gears-api offline: the cached adopted copies, stale.
func RunbookList(c Ctx, _ RunbookListIn) (*RunbookListOut, error) {
	books, err := runbook.All(c.Project)
	if err != nil {
		return nil, err
	}
	out := &RunbookListOut{Official: []api.OfficialRunbook{}, Adopted: []api.AdoptedRunbook{}, Drafts: []Runbook{}, Builtins: runbook.BuiltinSlugs(),
		Vocabulary: runbook.Checks, Rules: []string{
			"Runs live in byz-orchestrator behind gears-api (GRS-51): start one with runbook_start, advance it with runbook_step_complete or runbook_step_skip, read it with runbook_status or runbook_runs.",
			"A step completes only with evidence that exists in the workspace on gears-api: a session entry, an artifact, a desk item, a card (in a state), or a commit sha.",
			"Adopted runbooks are copies of official ones on gears-api (runbook_adopt): a frozen copy cannot be edited and runs the official steps; an editable copy changes with runbook_edit. Start either with runbook_start and its slug; runbook_show explains its steps.",
			"drafts are local definitions (runbook--<slug>.yaml) in the retired local vocabulary; they are not runnable as they stand.",
		}}
	for _, rb := range books {
		out.Drafts = append(out.Drafts, runbookOut(rb))
	}
	var apiErr error
	if ws, err := c.runsWorkspace(); err != nil {
		apiErr = err
	} else if cl, err := runsClient(); err != nil {
		apiErr = err
	} else {
		ctx, cancel := runCtx()
		defer cancel()
		mine, err := cl.AdoptedRunbooks(ctx, ws)
		if err == nil {
			for _, rb := range mine {
				_ = runs.SaveRunbook(c.Project, rb)
			}
			if mine != nil {
				out.Adopted = mine
			}
			off, err := cl.OfficialRunbooks(ctx, ws)
			var p *api.Problem
			switch {
			case err == nil:
				if off != nil {
					out.Official = off
				}
				out.OfficialAvailable = true
			case errors.As(err, &p) && p.Status == 501:
				out.Note = "the official catalog needs the runbook service, which this gears-api does not have; adopted copies are listed"
			default:
				out.Note = "the official catalog could not be read (" + err.Error() + "); adopted copies are listed"
			}
			return out, nil
		}
		apiErr = err
	}
	if out.Adopted, err = runs.Runbooks(c.Project); err != nil {
		return nil, err
	}
	if out.Adopted == nil {
		out.Adopted = []api.AdoptedRunbook{}
	}
	out.Stale = true
	out.Note = "gears-api did not answer (" + apiErr.Error() + "); adopted copies are the local cache and the official catalog is unknown"
	return out, nil
}

type RunbookShowIn struct {
	Runbook string `json:"runbook" jsonschema:"An adopted runbook's slug or an official runbook's ref (runbook_list). Locally also a runbook--<slug>.yaml draft, or a cached run id (run-...)."`
}

// RunbookShowOut is runbook_show's answer: the same fields as the remote
// MCP (api.RunbookDetail), plus draft or run when the name was a local
// draft or a cached run.
type RunbookShowOut struct {
	api.RunbookDetail
	Draft *Runbook `json:"draft,omitempty"`
	Run   *api.Run `json:"run,omitempty"`
}

// RunbookShow is one runbook in full from gears-api (GRS-70): an adopted
// slug or an official ref, each step explained from the step-kind catalog.
// gears-api unreachable: the cached adopted copy (stale). A name gears-api
// does not know may be a local draft; run-... reads the cached run.
func RunbookShow(c Ctx, in RunbookShowIn) (*RunbookShowOut, error) {
	key := strings.TrimSpace(in.Runbook)
	if key == "" {
		return nil, errors.New("runbook required: an adopted slug or an official ref (runbook_list)")
	}
	if strings.HasPrefix(strings.ToLower(key), "run-") {
		r, err := runs.Get(c.Project, key)
		if err != nil {
			return nil, err
		}
		return &RunbookShowOut{RunbookDetail: api.RunbookDetail{Runbook: r.Runbook, Source: "run", Title: r.Runbook, Steps: []api.RunbookDetailStep{}}, Run: r}, nil
	}
	var apiErr error
	if ws, err := c.runsWorkspace(); err != nil {
		apiErr = err
	} else if cl, err := runsClient(); err != nil {
		apiErr = err
	} else {
		ctx, cancel := runCtx()
		defer cancel()
		d, err := cl.RunbookDetail(ctx, ws, key)
		if err == nil {
			if d.Steps == nil {
				d.Steps = []api.RunbookDetailStep{}
			}
			return &RunbookShowOut{RunbookDetail: *d}, nil
		}
		apiErr = err
		var p *api.Problem
		if errors.As(err, &p) && p.Status != 404 && p.Status < 500 {
			return nil, err
		}
	}
	var p *api.Problem
	if !errors.As(apiErr, &p) {
		if ad, _ := runs.Runbook(c.Project, key); ad != nil {
			d := api.RunbookDetail{Runbook: ad.Slug, Source: "adopted", Title: ad.Title, Description: ad.Description, Frozen: ad.Frozen,
				Editable: !ad.Frozen, SourceRef: ad.SourceRef, SourceVersion: ad.SourceVersion, Steps: []api.RunbookDetailStep{}, Stale: true,
				Note: "gears-api did not answer (" + apiErr.Error() + "); this is the cached adopted copy, steps unexplained"}
			for _, st := range ad.Steps {
				kind := st.Kind
				if kind == "" {
					kind = "freeform"
				}
				d.Steps = append(d.Steps, api.RunbookDetailStep{ID: st.ID, Kind: kind, Title: st.Title, Params: st.Params})
			}
			return &RunbookShowOut{RunbookDetail: d}, nil
		}
	}
	rb, err := runbook.Load(c.Project, key)
	if err != nil {
		return nil, fmt.Errorf("no runbook %s: not adopted or official on gears-api (%v), and no local draft", key, apiErr)
	}
	o := runbookOut(rb)
	d := api.RunbookDetail{Runbook: rb.Slug, Source: "draft", Title: rb.Title, Editable: true, Steps: []api.RunbookDetailStep{},
		Note: "a local draft (runbook--" + rb.Slug + ".yaml) in the retired local vocabulary; not runnable as it stands"}
	for _, st := range o.Steps {
		d.Steps = append(d.Steps, api.RunbookDetailStep{ID: itoa(st.Idx), Kind: st.Kind, Title: st.Text})
	}
	return &RunbookShowOut{RunbookDetail: d, Draft: &o}, nil
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
