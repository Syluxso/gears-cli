// Package card is the local board: one board per project, shaped cards with
// byz-kan's types and sections, states as swimlanes. The rules here are the
// rules the API will enforce, so a card refused locally is refused remotely
// for the same reason.
package card

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
)

// Types of work. Exactly one per card. Empty means story.
var Types = []string{"story", "defect", "spike", "chore"}

// Section keys inside cardData.
const (
	SecStory      = "story"
	SecAcceptance = "acceptance"
	SecScenarios  = "scenarios"
	SecUAT        = "uat"
	SecDefect     = "defect"
	SecSpike      = "spike"
	SecChore      = "chore"
	SecSource     = "source"
)

// Required is what every card must carry, whatever its type: the story in
// the user's words and the acceptance that says when it is done. The create
// fails without both.
var Required = []string{SecStory, SecAcceptance}

// seed is the type's own block, created empty beside the required two so an
// agent sees the slot. offer is worth suggesting but never auto-created.
var seed = map[string][]string{
	"story":  {},
	"defect": {SecDefect},
	"spike":  {SecSpike},
	"chore":  {SecChore},
}
var offer = map[string][]string{
	"story":  {SecScenarios, SecUAT},
	"defect": {SecScenarios, SecUAT},
	"spike":  {},
	"chore":  {},
}

// Order fixes how sections are reported.
var Order = []string{SecStory, SecAcceptance, SecScenarios, SecUAT, SecDefect, SecSpike, SecChore}

// DefaultStates seeds a new board. The last one is terminal: moving a card
// there stamps completed_at.
var DefaultStates = []string{"Backlog", "In Progress", "Testing", "Completed"}

// State is a swimlane.
type State struct {
	ID       string
	Name     string
	Position int
	Default  bool
	Terminal bool
}

// Card is one row of the board.
type Card struct {
	ID          string
	Number      int
	Key         string
	Title       string
	Body        string
	Type        string
	State       string // state name
	StateID     string
	Priority    int
	Position    int
	CardData    map[string]any
	Artifact    string // artifact file this card points at, if any
	External    string // Jira or Kanboard key, later
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt time.Time
}

// Shaped lists the sections that carry content, in catalog order.
func (c Card) Shaped() []string {
	var out []string
	for _, k := range Order {
		if v, ok := c.CardData[k]; ok && filled(v) {
			out = append(out, k)
		}
	}
	return out
}

// Omitted lists offered sections the card does not have yet.
func (c Card) Omitted() []string {
	var out []string
	for _, k := range offer[c.Type] {
		if _, ok := c.CardData[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

func emptySection(key string) any {
	switch key {
	case SecStory:
		return map[string]any{"asA": "", "iWant": "", "soThat": ""}
	case SecAcceptance, SecScenarios, SecUAT:
		return []any{}
	case SecDefect:
		return map[string]any{"repro": "", "expected": "", "actual": ""}
	case SecSpike:
		return map[string]any{"question": "", "timeboxMinutes": 0, "approach": "", "findings": "", "outcome": "", "followUp": ""}
	case SecChore:
		return map[string]any{"why": "", "doneWhen": ""}
	}
	return map[string]any{}
}

// filled reports whether a section carries anything a reader would see.
func filled(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case []any:
		for _, x := range t {
			if filled(x) {
				return true
			}
		}
		return false
	case []string:
		for _, x := range t {
			if strings.TrimSpace(x) != "" {
				return true
			}
		}
		return false
	case map[string]any:
		for _, x := range t {
			if filled(x) {
				return true
			}
		}
		return false
	}
	return true
}

// NormalizeType canonicalises a type; empty means story.
func NormalizeType(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "ticket" {
		return "story", nil
	}
	for _, t := range Types {
		if t == s {
			return t, nil
		}
	}
	return "", fmt.Errorf("type %q; one of %s", s, strings.Join(Types, ", "))
}

// checkRequired is the refusal rule. A story needs all three parts; the
// acceptance list needs at least one non-blank line.
func checkRequired(data map[string]any) error {
	var missing []string
	st, _ := data[SecStory].(map[string]any)
	for _, k := range []string{"asA", "iWant", "soThat"} {
		if v, _ := st[k].(string); strings.TrimSpace(v) == "" {
			missing = append(missing, "story."+k)
		}
	}
	if !filled(data[SecAcceptance]) {
		missing = append(missing, "acceptance (at least one line)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("card refused; missing %s. Story and acceptance are required on every card", strings.Join(missing, ", "))
	}
	return nil
}

// ---------------------------------------------------------------- states

func ensureStates(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM card_states`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := store.Now()
	for i, name := range DefaultStates {
		if _, err := db.Exec(`INSERT INTO card_states (id, name, position, is_default, is_terminal, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			project.NewID(), name, i, b2i(i == 0), b2i(i == len(DefaultStates)-1), now, now); err != nil {
			return err
		}
	}
	return nil
}

func states(db *sql.DB) ([]State, error) {
	if err := ensureStates(db); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, name, position, is_default, is_terminal FROM card_states ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		var s State
		var d, t int
		if err := rows.Scan(&s.ID, &s.Name, &s.Position, &d, &t); err != nil {
			return nil, err
		}
		s.Default, s.Terminal = d == 1, t == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

// States lists the board's swimlanes in order.
func States(p *project.Project) ([]State, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return states(db)
}

func findState(list []State, name string) (*State, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for i := range list {
		if strings.ToLower(list[i].Name) == want || list[i].ID == name {
			return &list[i], nil
		}
	}
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	return nil, fmt.Errorf("no state %q; one of %s", name, strings.Join(names, ", "))
}

// ------------------------------------------------------------------- new

// Input is what a caller supplies to New.
type Input struct {
	Type     string
	Title    string
	Body     string
	CardData map[string]any // story, acceptance, plus any type section
	Priority int
	Artifact string
	Source   string // agent | user; default agent
}

// New mints a card. The key is <prefix>-<n>; numbers never reuse.
func New(p *project.Project, in Input, agent string) (*Card, error) {
	typ, err := NormalizeType(in.Type)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("title is empty")
	}
	data := map[string]any{}
	for k, v := range in.CardData {
		data[k] = v
	}
	// Seed the slots the type should show, without overwriting what came in.
	for _, k := range append(append([]string{}, Required...), seed[typ]...) {
		if _, ok := data[k]; !ok {
			data[k] = emptySection(k)
		}
	}
	if typ == "spike" {
		if sp, ok := data[SecSpike].(map[string]any); ok {
			if q, _ := sp["question"].(string); strings.TrimSpace(q) == "" {
				sp["question"] = title
			}
		}
	}
	if err := checkRequired(data); err != nil {
		return nil, err
	}
	src := strings.ToLower(strings.TrimSpace(in.Source))
	if src != "user" {
		src = "agent"
	}
	data[SecSource] = src
	if agent == "" {
		agent = session.DefaultAgent()
	}

	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sts, err := states(db)
	if err != nil {
		return nil, err
	}
	def := sts[0]
	for _, s := range sts {
		if s.Default {
			def = s
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var last int
	if err := tx.QueryRow(`SELECT last_number FROM card_sequence WHERE id = 1`).Scan(&last); err != nil {
		if err != sql.ErrNoRows {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO card_sequence (id, last_number) VALUES (1, 0)`); err != nil {
			return nil, err
		}
	}
	num := last + 1
	if _, err := tx.Exec(`UPDATE card_sequence SET last_number = ? WHERE id = 1`, num); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(data)
	now := store.Now()
	c := &Card{ID: project.NewID(), Number: num, Key: p.Prefix + "-" + strconv.Itoa(num), Title: title, Body: strings.TrimSpace(in.Body),
		Type: typ, State: def.Name, StateID: def.ID, Priority: in.Priority, CardData: data, Artifact: strings.TrimSpace(in.Artifact), CreatedBy: agent}
	if _, err := tx.Exec(`INSERT INTO cards (id, number, key, title, body, type, state_id, priority, position, card_data, artifact, external_key, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, '', ?, ?, ?)`,
		c.ID, c.Number, c.Key, c.Title, c.Body, c.Type, c.StateID, c.Priority, string(raw), c.Artifact, c.CreatedBy, now, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	c.CreatedAt, c.UpdatedAt = store.Parse(now), store.Parse(now)
	return c, nil
}

// ------------------------------------------------------------------ read

const selectCols = `SELECT c.id, c.number, c.key, c.title, c.body, c.type, c.state_id, s.name, c.priority, c.position, c.card_data,
	c.artifact, c.external_key, c.created_by, c.created_at, c.updated_at, c.completed_at
	FROM cards c JOIN card_states s ON s.id = c.state_id WHERE c.deleted_at = ''`

func scan(rows interface{ Scan(...any) error }) (*Card, error) {
	var c Card
	var raw, created, updated, completed string
	if err := rows.Scan(&c.ID, &c.Number, &c.Key, &c.Title, &c.Body, &c.Type, &c.StateID, &c.State, &c.Priority, &c.Position, &raw,
		&c.Artifact, &c.External, &c.CreatedBy, &created, &updated, &completed); err != nil {
		return nil, err
	}
	c.CardData = map[string]any{}
	_ = json.Unmarshal([]byte(raw), &c.CardData)
	c.CreatedAt, c.UpdatedAt = store.Parse(created), store.Parse(updated)
	if completed != "" {
		c.CompletedAt = store.Parse(completed)
	}
	return &c, nil
}

// Filter narrows a listing. Completed cards are hidden unless All or the
// terminal state is named.
type Filter struct {
	State string
	Type  string
	All   bool
}

// List returns cards ordered by state position, then card position, then number.
func List(p *project.Project, f Filter) ([]Card, error) {
	if !store.Exists(p) {
		return nil, nil
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sts, err := states(db)
	if err != nil {
		return nil, err
	}
	q := selectCols
	var args []any
	if f.State != "" {
		st, err := findState(sts, f.State)
		if err != nil {
			return nil, err
		}
		q += ` AND c.state_id = ?`
		args = append(args, st.ID)
	} else if !f.All {
		q += ` AND s.is_terminal = 0`
	}
	if f.Type != "" {
		t, err := NormalizeType(f.Type)
		if err != nil {
			return nil, err
		}
		q += ` AND c.type = ?`
		args = append(args, t)
	}
	q += ` ORDER BY s.position, c.position, c.number`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Card
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// Get finds a card by key (GRS-14, case-insensitive), bare number, id, or id prefix.
func Get(p *project.Project, key string) (*Card, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("card key is empty")
	}
	if !store.Exists(p) {
		return nil, fmt.Errorf("no card %q", key)
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := states(db); err != nil {
		return nil, err
	}
	if n, err := strconv.Atoi(key); err == nil {
		key = p.Prefix + "-" + strconv.Itoa(n)
	}
	rows, err := db.Query(selectCols+` AND (upper(c.key) = upper(?) OR c.id = ? OR c.id LIKE ?)`, key, key, key+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hit *Card
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if hit != nil {
			return nil, fmt.Errorf("%q is ambiguous", key)
		}
		hit = c
	}
	if hit == nil {
		return nil, fmt.Errorf("no card %q", key)
	}
	return hit, nil
}

// Exists reports whether a key names a card; runs and desk refs use it.
func Exists(p *project.Project, key string) bool {
	_, err := Get(p, key)
	return err == nil
}

// ---------------------------------------------------------------- update

// Patch is a partial update; nil pointers leave a field alone. CardData is
// merged key by key: sent keys replace, omitted keys stay. The result must
// still pass the required rule.
type Patch struct {
	Title    *string
	Body     *string
	Type     *string
	Priority *int
	Artifact *string
	External *string
	CardData map[string]any
}

// Update applies a patch and re-checks the required rule.
func Update(p *project.Project, key string, patch Patch, agent string) (*Card, error) {
	c, err := Get(p, key)
	if err != nil {
		return nil, err
	}
	if patch.Title != nil {
		if strings.TrimSpace(*patch.Title) == "" {
			return nil, fmt.Errorf("title is empty")
		}
		c.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Body != nil {
		c.Body = strings.TrimSpace(*patch.Body)
	}
	if patch.Type != nil {
		t, err := NormalizeType(*patch.Type)
		if err != nil {
			return nil, err
		}
		c.Type = t
		for _, k := range seed[t] {
			if _, ok := c.CardData[k]; !ok {
				c.CardData[k] = emptySection(k)
			}
		}
	}
	if patch.Priority != nil {
		c.Priority = *patch.Priority
	}
	if patch.Artifact != nil {
		c.Artifact = strings.TrimSpace(*patch.Artifact)
	}
	if patch.External != nil {
		c.External = strings.TrimSpace(*patch.External)
	}
	for k, v := range patch.CardData {
		c.CardData[k] = v
	}
	if err := checkRequired(c.CardData); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(c.CardData)
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	now := store.Now()
	if _, err := db.Exec(`UPDATE cards SET title = ?, body = ?, type = ?, priority = ?, artifact = ?, external_key = ?, card_data = ?, updated_at = ? WHERE id = ?`,
		c.Title, c.Body, c.Type, c.Priority, c.Artifact, c.External, string(raw), now, c.ID); err != nil {
		return nil, err
	}
	c.UpdatedAt = store.Parse(now)
	return c, nil
}

// Move puts a card in another state. Entering the terminal state stamps
// completed_at; leaving it clears the stamp.
func Move(p *project.Project, key, state, agent string) (*Card, error) {
	c, err := Get(p, key)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sts, err := states(db)
	if err != nil {
		return nil, err
	}
	st, err := findState(sts, state)
	if err != nil {
		return nil, err
	}
	now := store.Now()
	completed := ""
	if st.Terminal {
		completed = now
	}
	if _, err := db.Exec(`UPDATE cards SET state_id = ?, completed_at = ?, updated_at = ? WHERE id = ?`, st.ID, completed, now, c.ID); err != nil {
		return nil, err
	}
	c.StateID, c.State, c.UpdatedAt = st.ID, st.Name, store.Parse(now)
	c.CompletedAt = time.Time{}
	if completed != "" {
		c.CompletedAt = store.Parse(completed)
	}
	return c, nil
}

// Delete soft-deletes a card. The number is never reused.
func Delete(p *project.Project, key, agent string) (*Card, error) {
	c, err := Get(p, key)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	now := store.Now()
	if _, err := db.Exec(`UPDATE cards SET deleted_at = ?, updated_at = ? WHERE id = ?`, now, now, c.ID); err != nil {
		return nil, err
	}
	return c, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
