package verbs

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Syluxso/gears-cli/internal/card"
)

// ------------------------------------------------------------------ card

// Card is the JSON shape of one card, for both doors.
type Card struct {
	ID          string         `json:"id"`
	Key         string         `json:"key"`
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	TopLine     string         `json:"topLine"`
	Type        string         `json:"type"`
	State       string         `json:"state"`
	Size        string         `json:"size,omitempty"`
	Priority    string         `json:"priority"`
	Position    int            `json:"position"`
	Tags        []string       `json:"tags"`
	Assignee    string         `json:"assignee,omitempty"`
	Parent      string         `json:"parent,omitempty"`
	DueAt       string         `json:"dueAt,omitempty"`
	Estimate    int            `json:"estimateMinutes,omitempty"`
	Body        string         `json:"body,omitempty"`
	CardData    map[string]any `json:"cardData"`
	Shaped      []string       `json:"shaped"`
	Omitted     []string       `json:"omitted"`
	Artifact    string         `json:"artifact,omitempty"`
	External    string         `json:"externalKey,omitempty"`
	CreatedBy   string         `json:"createdBy"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
	CompletedAt string         `json:"completedAt,omitempty"`
}

func cardOut(c card.Card) Card {
	return Card{ID: c.ID, Key: c.Key, Number: c.Number, Title: c.Title, TopLine: c.TopLine(), Type: c.Type, State: c.State,
		Size: c.Size, Priority: "P" + strconv.Itoa(c.Priority), Position: c.Position, Tags: nz(c.Tags), Assignee: c.Assignee,
		Parent: c.ParentKey, DueAt: ts(c.DueAt), Estimate: c.Estimate, Body: c.Body, CardData: c.CardData,
		Shaped: nz(c.Shaped()), Omitted: nz(c.Omitted()), Artifact: c.Artifact, External: c.External, CreatedBy: c.CreatedBy,
		CreatedAt: ts(c.CreatedAt), UpdatedAt: ts(c.UpdatedAt), CompletedAt: ts(c.CompletedAt)}
}

type CardComment struct {
	ID        string `json:"id"`
	Agent     string `json:"agent"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

type CardLink struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // blocks | blocked-by | related | ref
	Target    string `json:"target,omitempty"`
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	Agent     string `json:"agent"`
	CreatedAt string `json:"createdAt"`
}

type CardChecklistItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Done   bool   `json:"done"`
	DoneBy string `json:"doneBy,omitempty"`
	DoneAt string `json:"doneAt,omitempty"`
}

type CardChecklist struct {
	ID    string              `json:"id"`
	Title string              `json:"title"`
	Done  int                 `json:"done"`
	Total int                 `json:"total"`
	Items []CardChecklistItem `json:"items"`
}

type CardEvent struct {
	At     string `json:"at"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Card   string `json:"card,omitempty"`
}

func checklistOut(cl card.Checklist) CardChecklist {
	out := CardChecklist{ID: cl.ID, Title: cl.Title, Total: len(cl.Items), Items: []CardChecklistItem{}}
	for _, it := range cl.Items {
		if it.Done {
			out.Done++
		}
		out.Items = append(out.Items, CardChecklistItem{ID: it.ID, Title: it.Title, Done: it.Done, DoneBy: it.DoneBy, DoneAt: ts(it.DoneAt)})
	}
	return out
}

// CardFull is a card with its trail.
type CardRun struct {
	ID      string `json:"id"`
	Runbook string `json:"runbook"`
	Agent   string `json:"agent"`
	Step    string `json:"step,omitempty"`
}

type CardFull struct {
	Card
	Run        *CardRun        `json:"run,omitempty"`
	BlockedBy  []string        `json:"blockedBy"`
	Comments   []CardComment   `json:"comments"`
	Links      []CardLink      `json:"links"`
	Checklists []CardChecklist `json:"checklists"`
	Children   []Card          `json:"children"`
	Events     []CardEvent     `json:"events"`
}

func fullOut(f *card.Full) *CardFull {
	out := &CardFull{Card: cardOut(f.Card), BlockedBy: nz(f.Blocked()), Comments: []CardComment{}, Links: []CardLink{},
		Checklists: []CardChecklist{}, Children: []Card{}, Events: []CardEvent{}}
	if f.Run != nil {
		out.Run = &CardRun{ID: f.Run.ID, Runbook: f.Run.Runbook, Agent: f.Run.Agent, Step: f.Run.Step}
	}
	for _, cm := range f.Comments {
		out.Comments = append(out.Comments, CardComment{ID: cm.ID, Agent: cm.Agent, Body: cm.Body, CreatedAt: ts(cm.CreatedAt)})
	}
	for _, l := range f.Links {
		out.Links = append(out.Links, CardLink{ID: l.ID, Kind: l.Kind, Target: l.Target, URL: l.URL, Title: l.Title, Agent: l.Agent, CreatedAt: ts(l.CreatedAt)})
	}
	for _, cl := range f.Checklists {
		out.Checklists = append(out.Checklists, checklistOut(cl))
	}
	for _, ch := range f.Children {
		out.Children = append(out.Children, cardOut(ch))
	}
	for _, e := range f.Events {
		out.Events = append(out.Events, CardEvent{At: ts(e.At), Agent: e.Agent, Kind: e.Kind, Detail: e.Detail})
	}
	return out
}

// CardStory is the convenience form of the story section.
type CardStory struct {
	AsA    string `json:"asA" jsonschema:"Who wants this, in their own words."`
	IWant  string `json:"iWant" jsonschema:"What they want to be able to do."`
	SoThat string `json:"soThat" jsonschema:"Why it matters to them."`
}

type CardNewIn struct {
	Type       string         `json:"type,omitempty" jsonschema:"story (default) | defect | task | spike | chore | epic. Each needs its own top-line section: story{asA,iWant,soThat}; defect{symptom,expected,actual,repro?}; task{what,soThat}; spike{question,...}; chore{what,why}; epic{outcome}."`
	Title      string         `json:"title" jsonschema:"Short heading."`
	Story      *CardStory     `json:"story,omitempty" jsonschema:"The story section, for type story. Or put it in sections.story."`
	Acceptance []string       `json:"acceptance" jsonschema:"Required: done-when lines, each testable. Write 'X, proven by Y' so the proof is the UAT."`
	Sections   map[string]any `json:"sections,omitempty" jsonschema:"Other sections by key: defect, task, spike, chore, epic, brief{state,refs[],files[],prereqs[],rules[],gotchas[],escalate}, scenarios[{name,given,when,then}], uat[]. Unknown keys or fields are refused; see card_shapes."`
	Body       string         `json:"body,omitempty" jsonschema:"Original text or evidence. Logs and traces go in comments."`
	Size       string         `json:"size,omitempty" jsonschema:"S (under a day) | M (a few days) | L (a week or more). A proposal for the owner."`
	Priority   string         `json:"priority,omitempty" jsonschema:"P0 | P1 | P2 | P3. P3 is the highest. Default P0."`
	Tags       []string       `json:"tags,omitempty" jsonschema:"Slices of the board (module, customer, theme). Reuse before inventing."`
	Assignee   string         `json:"assignee,omitempty" jsonschema:"Agent label who owns it."`
	Parent     string         `json:"parent,omitempty" jsonschema:"Parent card key, usually an epic."`
	DueAt      string         `json:"dueAt,omitempty" jsonschema:"YYYY-MM-DD or RFC3339."`
	Estimate   int            `json:"estimateMinutes,omitempty"`
	Artifact   string         `json:"artifact,omitempty" jsonschema:"Artifact file this card points at, e.g. story--login.md."`
	Source     string         `json:"source,omitempty" jsonschema:"agent (default) if you inferred the shape; user if a person said it."`
}

// CardNew mints a card in the default state. Refused unless the type's
// top-line section and at least one acceptance line are present.
func CardNew(c Ctx, in CardNewIn) (*Card, error) {
	data := map[string]any{}
	for k, v := range in.Sections {
		data[k] = v
	}
	if in.Story != nil {
		data[card.SecStory] = map[string]any{"asA": in.Story.AsA, "iWant": in.Story.IWant, "soThat": in.Story.SoThat}
	}
	if in.Acceptance != nil {
		data[card.SecAcceptance] = anyList(in.Acceptance)
	}
	out, err := card.New(c.Project, card.Input{Type: in.Type, Title: in.Title, Body: in.Body, CardData: data, Size: in.Size,
		Priority: in.Priority, Tags: in.Tags, Assignee: in.Assignee, Parent: in.Parent, DueAt: in.DueAt, Estimate: in.Estimate,
		Artifact: in.Artifact, Source: in.Source}, c.agent())
	if err != nil {
		return nil, err
	}
	o := cardOut(*out)
	return &o, nil
}

func anyList(list []string) []any {
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

type CardListIn struct {
	State    string `json:"state,omitempty" jsonschema:"Only cards in this state, by name."`
	Type     string `json:"type,omitempty" jsonschema:"Only this type."`
	Tag      string `json:"tag,omitempty" jsonschema:"Only cards carrying this tag."`
	Assignee string `json:"assignee,omitempty" jsonschema:"Only cards owned by this agent label. 'me' means the calling agent."`
	Parent   string `json:"parent,omitempty" jsonschema:"Only children of this card."`
	Query    string `json:"q,omitempty" jsonschema:"Substring of key, title, body or sections. Use before creating a card: it may exist."`
	All      bool   `json:"all,omitempty" jsonschema:"Include completed cards."`
}

type CardState struct {
	Name     string `json:"name"`
	Count    int    `json:"count"`
	Default  bool   `json:"default,omitempty"`
	Terminal bool   `json:"terminal,omitempty"`
	WIPLimit int    `json:"wipLimit,omitempty"`
}

type CardListOut struct {
	Prefix string         `json:"prefix"`
	States []CardState    `json:"states"`
	Tags   map[string]int `json:"tags"`
	Cards  []Card         `json:"cards"`
}

// CardList is the board: states in order with counts, tags in use, cards.
func CardList(c Ctx, in CardListIn) (*CardListOut, error) {
	if in.Assignee == "me" {
		in.Assignee = c.agent()
	}
	sts, err := card.States(c.Project)
	if err != nil {
		return nil, err
	}
	cards, err := card.List(c.Project, card.Filter{State: in.State, Type: in.Type, Tag: in.Tag, Assignee: in.Assignee, Parent: in.Parent, Query: in.Query, All: in.All})
	if err != nil {
		return nil, err
	}
	tags, err := card.Tags(c.Project)
	if err != nil {
		return nil, err
	}
	out := &CardListOut{Prefix: c.Project.Prefix, States: []CardState{}, Tags: tags, Cards: []Card{}}
	counts := map[string]int{}
	for _, cd := range cards {
		counts[cd.State]++
		out.Cards = append(out.Cards, cardOut(cd))
	}
	for _, s := range sts {
		out.States = append(out.States, CardState{Name: s.Name, Count: counts[s.Name], Default: s.Default, Terminal: s.Terminal, WIPLimit: s.WIPLimit})
	}
	return out, nil
}

type CardKeyIn struct {
	Key string `json:"key" jsonschema:"Card key like GRS-14, a bare number, or the id."`
}

// CardShow is the whole card: sections, comments, links, checklists,
// children and the activity trail. Read it before picking a card up.
func CardShow(c Ctx, in CardKeyIn) (*CardFull, error) {
	f, err := card.Show(c.Project, in.Key)
	if err != nil {
		return nil, err
	}
	return fullOut(f), nil
}

type CardUpdateIn struct {
	Key        string         `json:"key" jsonschema:"Card key, number or id."`
	Title      *string        `json:"title,omitempty"`
	Body       *string        `json:"body,omitempty"`
	Type       *string        `json:"type,omitempty" jsonschema:"story | defect | task | spike | chore | epic. The new type's section is seeded if absent; fill it or the update is refused."`
	Size       *string        `json:"size,omitempty" jsonschema:"S | M | L; empty clears."`
	Priority   *string        `json:"priority,omitempty" jsonschema:"P0..P3, P3 highest."`
	Assignee   *string        `json:"assignee,omitempty" jsonschema:"Agent label; empty clears."`
	Parent     *string        `json:"parent,omitempty" jsonschema:"Parent card key; empty clears."`
	DueAt      *string        `json:"dueAt,omitempty" jsonschema:"YYYY-MM-DD or RFC3339; empty clears."`
	Estimate   *int           `json:"estimateMinutes,omitempty"`
	Artifact   *string        `json:"artifact,omitempty" jsonschema:"Artifact file to point at; empty clears."`
	External   *string        `json:"externalKey,omitempty" jsonschema:"Jira or Kanboard key; empty clears."`
	Story      *CardStory     `json:"story,omitempty" jsonschema:"Replaces the story section."`
	Acceptance []string       `json:"acceptance,omitempty" jsonschema:"Replaces the acceptance list."`
	Sections   map[string]any `json:"sections,omitempty" jsonschema:"Sections merged key by key: a key you send replaces that section whole; keys you omit stay."`
}

// CardUpdate changes only the fields sent. The card must still pass its
// type's rule afterwards. Every change lands in the activity trail.
func CardUpdate(c Ctx, in CardUpdateIn) (*Card, error) {
	data := map[string]any{}
	for k, v := range in.Sections {
		data[k] = v
	}
	if in.Story != nil {
		data[card.SecStory] = map[string]any{"asA": in.Story.AsA, "iWant": in.Story.IWant, "soThat": in.Story.SoThat}
	}
	if in.Acceptance != nil {
		data[card.SecAcceptance] = anyList(in.Acceptance)
	}
	cd, err := card.Update(c.Project, in.Key, card.Patch{Title: in.Title, Body: in.Body, Type: in.Type, Size: in.Size, Priority: in.Priority,
		Assignee: in.Assignee, Parent: in.Parent, DueAt: in.DueAt, Estimate: in.Estimate, Artifact: in.Artifact, External: in.External, CardData: data}, c.agent())
	if err != nil {
		return nil, err
	}
	o := cardOut(*cd)
	return &o, nil
}

type CardMoveIn struct {
	Key      string `json:"key" jsonschema:"Card key, number or id."`
	State    string `json:"state,omitempty" jsonschema:"Target state by name. Empty keeps the state and only reorders."`
	Position string `json:"position,omitempty" jsonschema:"top | bottom (default) | after:<key>."`
}

type CardMoveOut struct {
	Card    Card   `json:"card"`
	Warning string `json:"warning,omitempty"`
}

// CardMove changes state or order. Entering the terminal state stamps
// completedAt. A WIP limit overrun is a warning, not a refusal.
func CardMove(c Ctx, in CardMoveIn) (*CardMoveOut, error) {
	cd, warn, err := card.Move(c.Project, in.Key, in.State, in.Position, c.agent())
	if err != nil {
		return nil, err
	}
	return &CardMoveOut{Card: cardOut(*cd), Warning: warn}, nil
}

// CardClaim is "I'm on it": assign to this agent and move out of the
// default state into the next one.
func CardClaim(c Ctx, in CardKeyIn) (*CardMoveOut, error) {
	cd, warn, err := card.Claim(c.Project, in.Key, c.agent())
	if err != nil {
		return nil, err
	}
	return &CardMoveOut{Card: cardOut(*cd), Warning: warn}, nil
}

// CardDelete soft-deletes; the number is never reused.
func CardDelete(c Ctx, in CardKeyIn) (*Card, error) {
	cd, err := card.Delete(c.Project, in.Key, c.agent())
	if err != nil {
		return nil, err
	}
	o := cardOut(*cd)
	return &o, nil
}

type CardCommentIn struct {
	Key  string `json:"key" jsonschema:"Card key, number or id."`
	Body string `json:"body" jsonschema:"Evidence or a note: findings, a log excerpt, a decision, what you tried. Markdown."`
}

func CardCommentAdd(c Ctx, in CardCommentIn) (*CardComment, error) {
	cm, err := card.AddComment(c.Project, in.Key, in.Body, c.agent())
	if err != nil {
		return nil, err
	}
	return &CardComment{ID: cm.ID, Agent: cm.Agent, Body: cm.Body, CreatedAt: ts(cm.CreatedAt)}, nil
}

type CardTagIn struct {
	Key    string   `json:"key" jsonschema:"Card key, number or id."`
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

func CardTag(c Ctx, in CardTagIn) (*Card, error) {
	cd, err := card.Tag(c.Project, in.Key, in.Add, in.Remove, c.agent())
	if err != nil {
		return nil, err
	}
	o := cardOut(*cd)
	return &o, nil
}

type CardLinkIn struct {
	Key    string `json:"key" jsonschema:"Card key, number or id."`
	Op     string `json:"op,omitempty" jsonschema:"add (default) | remove."`
	Kind   string `json:"kind,omitempty" jsonschema:"blocks | blocked-by | related | ref (a URL). Required for add."`
	Target string `json:"target,omitempty" jsonschema:"Other card's key, for blocks, blocked-by, related."`
	URL    string `json:"url,omitempty" jsonschema:"For ref."`
	Title  string `json:"title,omitempty" jsonschema:"For ref."`
	Link   string `json:"link,omitempty" jsonschema:"Link id (or prefix) to remove."`
}

type CardLinkOut struct {
	Link  *CardLink  `json:"link,omitempty"`
	Links []CardLink `json:"links"`
}

// CardLink adds or removes a link, then returns the card's links.
func CardLinkOp(c Ctx, in CardLinkIn) (*CardLinkOut, error) {
	out := &CardLinkOut{Links: []CardLink{}}
	switch strings.ToLower(in.Op) {
	case "", "add":
		l, err := card.AddLink(c.Project, in.Key, in.Kind, in.Target, in.URL, in.Title, c.agent())
		if err != nil {
			return nil, err
		}
		out.Link = &CardLink{ID: l.ID, Kind: l.Kind, Target: l.Target, URL: l.URL, Title: l.Title, Agent: l.Agent, CreatedAt: ts(l.CreatedAt)}
	case "remove":
		if err := card.RemoveLink(c.Project, in.Key, in.Link, c.agent()); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("op %q; add or remove", in.Op)
	}
	f, err := card.Show(c.Project, in.Key)
	if err != nil {
		return nil, err
	}
	out.Links = fullOut(f).Links
	return out, nil
}

type CardChecklistIn struct {
	Key       string   `json:"key" jsonschema:"Card key, number or id."`
	Op        string   `json:"op" jsonschema:"add (a checklist) | item (append one) | tick | untick | remove (the checklist)."`
	Checklist string   `json:"checklist,omitempty" jsonschema:"Checklist title or id. Default UAT for add; the only checklist otherwise."`
	Items     []string `json:"items,omitempty" jsonschema:"For add: initial items."`
	Item      string   `json:"item,omitempty" jsonschema:"For item: the text. For tick/untick: title, id prefix, or 1-based index."`
}

type CardChecklistOut struct {
	Checklists []CardChecklist `json:"checklists"`
}

// CardChecklist manages what a human ticks off. byz-kan CW-35: the uat
// section stays text; a checklist is created deliberately when someone
// should tick things.
func CardChecklistOp(c Ctx, in CardChecklistIn) (*CardChecklistOut, error) {
	var err error
	switch strings.ToLower(in.Op) {
	case "add":
		_, err = card.AddChecklist(c.Project, in.Key, in.Checklist, in.Items, c.agent())
	case "item":
		_, err = card.AddItem(c.Project, in.Key, in.Checklist, in.Item, c.agent())
	case "tick":
		_, err = card.Tick(c.Project, in.Key, in.Checklist, in.Item, true, c.agent())
	case "untick":
		_, err = card.Tick(c.Project, in.Key, in.Checklist, in.Item, false, c.agent())
	case "remove":
		err = card.RemoveChecklist(c.Project, in.Key, in.Checklist, c.agent())
	default:
		return nil, fmt.Errorf("op %q; one of add, item, tick, untick, remove", in.Op)
	}
	if err != nil {
		return nil, err
	}
	f, err := card.Show(c.Project, in.Key)
	if err != nil {
		return nil, err
	}
	return &CardChecklistOut{Checklists: fullOut(f).Checklists}, nil
}

type CardStatesIn struct {
	Op       string   `json:"op,omitempty" jsonschema:"list (default) | add | rename | set | reorder | remove."`
	Name     string   `json:"name,omitempty" jsonschema:"State to add, rename, set or remove."`
	NewName  string   `json:"newName,omitempty" jsonschema:"For rename."`
	WIPLimit *int     `json:"wipLimit,omitempty" jsonschema:"For add or set: 0 means none."`
	Default  *bool    `json:"default,omitempty" jsonschema:"For set: make this the default (ready) state."`
	Terminal *bool    `json:"terminal,omitempty" jsonschema:"For set: make this the terminal (done) state, or clear it with false."`
	Order    []string `json:"order,omitempty" jsonschema:"For reorder: every state name, in order."`
}

type CardStatesOut struct {
	States []CardState `json:"states"`
}

// CardStates manages the swimlanes. One default, one terminal.
func CardStates(c Ctx, in CardStatesIn) (*CardStatesOut, error) {
	var sts []card.State
	var err error
	switch strings.ToLower(in.Op) {
	case "", "list":
		sts, err = card.States(c.Project)
	case "add":
		wip := 0
		if in.WIPLimit != nil {
			wip = *in.WIPLimit
		}
		sts, err = card.AddState(c.Project, in.Name, wip)
	case "rename":
		sts, err = card.UpdateState(c.Project, in.Name, card.StatePatch{Name: &in.NewName})
	case "set":
		sts, err = card.UpdateState(c.Project, in.Name, card.StatePatch{WIPLimit: in.WIPLimit, Default: in.Default, Terminal: in.Terminal})
	case "reorder":
		sts, err = card.ReorderStates(c.Project, in.Order)
	case "remove":
		sts, err = card.RemoveState(c.Project, in.Name)
	default:
		return nil, fmt.Errorf("op %q; one of list, add, rename, set, reorder, remove", in.Op)
	}
	if err != nil {
		return nil, err
	}
	cards, err := card.List(c.Project, card.Filter{All: true})
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, cd := range cards {
		counts[cd.State]++
	}
	out := &CardStatesOut{States: []CardState{}}
	for _, s := range sts {
		out.States = append(out.States, CardState{Name: s.Name, Count: counts[s.Name], Default: s.Default, Terminal: s.Terminal, WIPLimit: s.WIPLimit})
	}
	return out, nil
}

type CardShapesIn struct{}

type CardTypeShape struct {
	Type    string   `json:"type"`
	TopLine string   `json:"topLine"` // required section
	Seeded  []string `json:"seeded"`  // created empty
	Offered []string `json:"offered"` // suggested, never created
	Example string   `json:"example"`
}

type CardSectionShape struct {
	Key    string   `json:"key"`
	Kind   string   `json:"kind"`
	Fields []string `json:"fields,omitempty"`
	Lists  []string `json:"lists,omitempty"`
	Hint   string   `json:"hint"`
}

type CardShapesOut struct {
	Rule       string             `json:"rule"`
	Types      []CardTypeShape    `json:"types"`
	Sections   []CardSectionShape `json:"sections"`
	Sizes      []string           `json:"sizes"`
	Priorities []string           `json:"priorities"`
	LinkKinds  []string           `json:"linkKinds"`
}

var typeExamples = map[string]string{
	"story":  "Story: As a staff member, I want to sign in with Google, so that I have no second password.",
	"defect": "Bug: drafts fail to send from Outlook. Expected the draft to send, got a 500. Repro: open a draft, click send.",
	"task":   "Task: rotate the IAM signing key, so that the old key stops being trusted.",
	"spike":  "Question: which port does gears-api take? Timebox 30 min.",
	"chore":  "Chore: prune stale branches, so that the repo stays readable.",
	"epic":   "Epic: agents and people share one board.",
}

// CardShapes is the catalog an agent reads before its first card_new.
func CardShapes(c Ctx, _ CardShapesIn) (*CardShapesOut, error) {
	out := &CardShapesOut{
		Rule:       "Every card needs its type's top-line section and at least one acceptance line, or the create is refused. Write what the person said; a missing field is a question to ask, not a sentence to invent. Logs and traces go in comments, not the body.",
		Types:      []CardTypeShape{},
		Sections:   []CardSectionShape{},
		Sizes:      card.Sizes,
		Priorities: card.Priorities,
		LinkKinds:  append([]string{}, card.LinkKinds...),
	}
	for _, t := range card.Types {
		out.Types = append(out.Types, CardTypeShape{Type: t, TopLine: card.TopLineSection(t), Seeded: nz(card.Seeded(t)), Offered: nz(card.Offered(t)), Example: typeExamples[t]})
	}
	for _, s := range card.Catalog {
		out.Sections = append(out.Sections, CardSectionShape{Key: s.Key, Kind: s.Kind, Fields: s.Fields, Lists: s.Lists, Hint: s.Hint})
	}
	return out, nil
}

type CardActivityIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"How many events, newest first. Default 30."`
}

type CardActivityOut struct {
	Events []CardEvent `json:"events"`
}

// CardActivity is the board's recent trail across all cards.
func CardActivity(c Ctx, in CardActivityIn) (*CardActivityOut, error) {
	if in.Limit <= 0 {
		in.Limit = 30
	}
	evs, keys, err := card.Activity(c.Project, in.Limit)
	if err != nil {
		return nil, err
	}
	out := &CardActivityOut{Events: []CardEvent{}}
	for _, e := range evs {
		out.Events = append(out.Events, CardEvent{At: ts(e.At), Agent: e.Agent, Kind: e.Kind, Detail: e.Detail, Card: keys[e.ID]})
	}
	return out, nil
}
