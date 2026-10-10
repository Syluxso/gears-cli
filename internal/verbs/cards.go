package verbs

import (
	"github.com/Syluxso/gears-cli/internal/card"
)

// ------------------------------------------------------------------ card

// Card is the JSON shape of one card, for both doors.
type Card struct {
	ID          string         `json:"id"`
	Key         string         `json:"key"`
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	Body        string         `json:"body,omitempty"`
	Type        string         `json:"type"`
	State       string         `json:"state"`
	Priority    int            `json:"priority"`
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
	return Card{ID: c.ID, Key: c.Key, Number: c.Number, Title: c.Title, Body: c.Body, Type: c.Type, State: c.State,
		Priority: c.Priority, CardData: c.CardData, Shaped: nz(c.Shaped()), Omitted: nz(c.Omitted()),
		Artifact: c.Artifact, External: c.External, CreatedBy: c.CreatedBy,
		CreatedAt: ts(c.CreatedAt), UpdatedAt: ts(c.UpdatedAt), CompletedAt: ts(c.CompletedAt)}
}

type CardStory struct {
	AsA    string `json:"asA" jsonschema:"Who wants this, in their own words."`
	IWant  string `json:"iWant" jsonschema:"What they want to be able to do."`
	SoThat string `json:"soThat" jsonschema:"Why it matters to them."`
}

type CardNewIn struct {
	Type       string         `json:"type,omitempty" jsonschema:"story (default) | defect | spike | chore. Every type still needs story and acceptance."`
	Title      string         `json:"title" jsonschema:"Short heading."`
	Story      CardStory      `json:"story" jsonschema:"Required: asA, iWant, soThat all filled."`
	Acceptance []string       `json:"acceptance" jsonschema:"Required: at least one line that says when the card is done."`
	Body       string         `json:"body,omitempty" jsonschema:"Evidence: logs, snippets, links, history. Not the spec; that lives in the sections."`
	CardData   map[string]any `json:"cardData,omitempty" jsonschema:"Extra sections by key: defect{repro,expected,actual}; spike{question,timeboxMinutes,approach,findings,outcome,followUp}; chore{why,doneWhen}; scenarios[{name,given,when,then}]; uat[]."`
	Priority   int            `json:"priority,omitempty" jsonschema:"Any integer; higher is more urgent."`
	Artifact   string         `json:"artifact,omitempty" jsonschema:"Artifact file this card points at, e.g. story--login.md."`
	Source     string         `json:"source,omitempty" jsonschema:"agent (default) if you inferred the shape; user if a person said it."`
}

// CardNew mints a card in the default state. It refuses without a full
// story and at least one acceptance line.
func CardNew(c Ctx, in CardNewIn) (*Card, error) {
	data := map[string]any{}
	for k, v := range in.CardData {
		data[k] = v
	}
	data[card.SecStory] = map[string]any{"asA": in.Story.AsA, "iWant": in.Story.IWant, "soThat": in.Story.SoThat}
	acc := make([]any, 0, len(in.Acceptance))
	for _, a := range in.Acceptance {
		acc = append(acc, a)
	}
	data[card.SecAcceptance] = acc
	out, err := card.New(c.Project, card.Input{Type: in.Type, Title: in.Title, Body: in.Body, CardData: data,
		Priority: in.Priority, Artifact: in.Artifact, Source: in.Source}, c.agent())
	if err != nil {
		return nil, err
	}
	o := cardOut(*out)
	return &o, nil
}

type CardListIn struct {
	State string `json:"state,omitempty" jsonschema:"Only cards in this state, by name."`
	Type  string `json:"type,omitempty" jsonschema:"Only this type."`
	All   bool   `json:"all,omitempty" jsonschema:"Include completed cards."`
}

type CardState struct {
	Name     string `json:"name"`
	Count    int    `json:"count"`
	Default  bool   `json:"default,omitempty"`
	Terminal bool   `json:"terminal,omitempty"`
}

type CardListOut struct {
	Prefix string      `json:"prefix"`
	States []CardState `json:"states"`
	Cards  []Card      `json:"cards"`
}

// CardList is the board: states in order with counts, then the cards.
func CardList(c Ctx, in CardListIn) (*CardListOut, error) {
	sts, err := card.States(c.Project)
	if err != nil {
		return nil, err
	}
	cards, err := card.List(c.Project, card.Filter{State: in.State, Type: in.Type, All: in.All})
	if err != nil {
		return nil, err
	}
	out := &CardListOut{Prefix: c.Project.Prefix, States: []CardState{}, Cards: []Card{}}
	counts := map[string]int{}
	for _, cd := range cards {
		counts[cd.State]++
		out.Cards = append(out.Cards, cardOut(cd))
	}
	for _, s := range sts {
		out.States = append(out.States, CardState{Name: s.Name, Count: counts[s.Name], Default: s.Default, Terminal: s.Terminal})
	}
	return out, nil
}

type CardKeyIn struct {
	Key string `json:"key" jsonschema:"Card key like GRS-14, a bare number, or the id."`
}

func CardShow(c Ctx, in CardKeyIn) (*Card, error) {
	cd, err := card.Get(c.Project, in.Key)
	if err != nil {
		return nil, err
	}
	o := cardOut(*cd)
	return &o, nil
}

type CardUpdateIn struct {
	Key        string         `json:"key" jsonschema:"Card key, number or id."`
	Title      *string        `json:"title,omitempty"`
	Body       *string        `json:"body,omitempty"`
	Type       *string        `json:"type,omitempty" jsonschema:"story | defect | spike | chore"`
	Priority   *int           `json:"priority,omitempty"`
	Artifact   *string        `json:"artifact,omitempty" jsonschema:"Artifact file to point at; empty string clears."`
	External   *string        `json:"externalKey,omitempty" jsonschema:"Jira or Kanboard key; empty string clears."`
	Story      *CardStory     `json:"story,omitempty" jsonschema:"Replaces the story block."`
	Acceptance []string       `json:"acceptance,omitempty" jsonschema:"Replaces the acceptance list."`
	CardData   map[string]any `json:"cardData,omitempty" jsonschema:"Sections merged key by key: keys you send replace, keys you omit stay."`
}

// CardUpdate changes only the fields sent. The card must still pass the
// story-and-acceptance rule afterwards.
func CardUpdate(c Ctx, in CardUpdateIn) (*Card, error) {
	data := map[string]any{}
	for k, v := range in.CardData {
		data[k] = v
	}
	if in.Story != nil {
		data[card.SecStory] = map[string]any{"asA": in.Story.AsA, "iWant": in.Story.IWant, "soThat": in.Story.SoThat}
	}
	if in.Acceptance != nil {
		acc := make([]any, 0, len(in.Acceptance))
		for _, a := range in.Acceptance {
			acc = append(acc, a)
		}
		data[card.SecAcceptance] = acc
	}
	cd, err := card.Update(c.Project, in.Key, card.Patch{Title: in.Title, Body: in.Body, Type: in.Type, Priority: in.Priority,
		Artifact: in.Artifact, External: in.External, CardData: data}, c.agent())
	if err != nil {
		return nil, err
	}
	o := cardOut(*cd)
	return &o, nil
}

type CardMoveIn struct {
	Key   string `json:"key" jsonschema:"Card key, number or id."`
	State string `json:"state" jsonschema:"Target state by name: Backlog | In Progress | Testing | Completed (the defaults)."`
}

// CardMove changes state. Completed stamps completedAt.
func CardMove(c Ctx, in CardMoveIn) (*Card, error) {
	cd, err := card.Move(c.Project, in.Key, in.State, c.agent())
	if err != nil {
		return nil, err
	}
	o := cardOut(*cd)
	return &o, nil
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
