// Package card is the local board: one board per project, shaped cards in
// states, with the trail an agent needs to pick a card up cold: comments as
// evidence, links, checklists, tags, and an activity log. The rules here are
// the rules the API will enforce, so a card refused locally is refused
// remotely for the same reason. Shapes live in shapes.go.
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

// DefaultStates seeds a new board. The first is the default (ready work),
// the last is terminal: moving a card there stamps completed_at.
var DefaultStates = []string{"Backlog", "In Progress", "Testing", "Completed"}

// State is a swimlane.
type State struct {
	ID       string
	Name     string
	Position int
	Default  bool
	Terminal bool
	WIPLimit int
}

// Card is one row of the board.
type Card struct {
	ID          string
	Number      int
	Key         string
	Title       string
	Body        string
	Type        string
	State       string
	StateID     string
	Size        string // S | M | L | ""
	Priority    int    // 0..3, P3 highest
	Position    int
	CardData    map[string]any
	Tags        []string
	Assignee    string // agent label
	ParentKey   string // epic or parent card
	DueAt       time.Time
	Estimate    int // minutes
	Artifact    string
	External    string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt time.Time
}

// Shaped lists sections that carry content, in catalog order.
func (c Card) Shaped() []string {
	var out []string
	for _, s := range Catalog {
		if v, ok := c.CardData[s.Key]; ok && Filled(v) {
			out = append(out, s.Key)
		}
	}
	return out
}

// Omitted lists offered sections the card does not have yet, plus seeded
// sections still empty, so the agent knows what is worth adding.
func (c Card) Omitted() []string {
	var out []string
	for _, k := range Seeded(c.Type) {
		if !Filled(c.CardData[k]) {
			out = append(out, k)
		}
	}
	for _, k := range Offered(c.Type) {
		if _, ok := c.CardData[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// TopLine is the scannable first line.
func (c Card) TopLine() string { return TopLine(c.Type, c.CardData) }

// Comment is evidence on a card: logs, findings, decisions, history.
type Comment struct {
	ID        string
	CardKey   string
	Body      string
	Agent     string
	CreatedAt time.Time
}

// Link joins a card to another card (blocks, related, parent is separate)
// or to a URL (ref).
type Link struct {
	ID        string
	CardKey   string
	Kind      string // blocks | blocked-by (derived) | related | ref
	Target    string // card key for blocks/related
	URL       string
	Title     string
	Agent     string
	CreatedAt time.Time
}

// LinkKinds a caller may add. blocked-by is the reverse view of blocks.
var LinkKinds = []string{"blocks", "related", "ref"}

// Checklist is something a human ticks off (UAT, steps).
type Checklist struct {
	ID       string
	CardKey  string
	Title    string
	Position int
	Items    []ChecklistItem
}

type ChecklistItem struct {
	ID       string
	Title    string
	Done     bool
	Position int
	DoneAt   time.Time
	DoneBy   string
}

// Event is one line of a card's activity trail.
type Event struct {
	ID     int64
	At     time.Time
	Agent  string
	Kind   string // created | updated | moved | commented | tagged | linked | checklist | assigned | deleted
	Detail string
}

func agentOr(a string) string {
	if a == "" {
		return session.DefaultAgent()
	}
	return a
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
		if _, err := db.Exec(`INSERT INTO card_states (id, name, position, is_default, is_terminal, wip_limit, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
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
	rows, err := db.Query(`SELECT id, name, position, is_default, is_terminal, wip_limit FROM card_states ORDER BY position, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		var s State
		var d, t int
		if err := rows.Scan(&s.ID, &s.Name, &s.Position, &d, &t, &s.WIPLimit); err != nil {
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

// AddState appends a swimlane before the terminal state (or at the end).
func AddState(p *project.Project, name string, wip int) ([]State, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("state name is empty")
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
	if _, err := findState(sts, name); err == nil {
		return nil, fmt.Errorf("state %q exists", name)
	}
	pos := len(sts)
	if n := len(sts); n > 0 && sts[n-1].Terminal {
		pos = sts[n-1].Position
		if _, err := db.Exec(`UPDATE card_states SET position = position + 1, updated_at = ? WHERE position >= ?`, store.Now(), pos); err != nil {
			return nil, err
		}
	}
	now := store.Now()
	if _, err := db.Exec(`INSERT INTO card_states (id, name, position, is_default, is_terminal, wip_limit, created_at, updated_at) VALUES (?, ?, ?, 0, 0, ?, ?, ?)`,
		project.NewID(), name, pos, wip, now, now); err != nil {
		return nil, err
	}
	return states(db)
}

// StatePatch changes one state. Nil leaves a field alone.
type StatePatch struct {
	Name     *string
	WIPLimit *int
	Default  *bool
	Terminal *bool
}

// UpdateState renames or reflags a state. Exactly one default and at most
// one terminal are kept: setting one clears the previous holder.
func UpdateState(p *project.Project, name string, patch StatePatch) ([]State, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sts, err := states(db)
	if err != nil {
		return nil, err
	}
	st, err := findState(sts, name)
	if err != nil {
		return nil, err
	}
	now := store.Now()
	if patch.Name != nil {
		n := strings.TrimSpace(*patch.Name)
		if n == "" {
			return nil, fmt.Errorf("state name is empty")
		}
		if other, err := findState(sts, n); err == nil && other.ID != st.ID {
			return nil, fmt.Errorf("state %q exists", n)
		}
		if _, err := db.Exec(`UPDATE card_states SET name = ?, updated_at = ? WHERE id = ?`, n, now, st.ID); err != nil {
			return nil, err
		}
	}
	if patch.WIPLimit != nil {
		if _, err := db.Exec(`UPDATE card_states SET wip_limit = ?, updated_at = ? WHERE id = ?`, *patch.WIPLimit, now, st.ID); err != nil {
			return nil, err
		}
	}
	if patch.Default != nil && *patch.Default {
		if _, err := db.Exec(`UPDATE card_states SET is_default = CASE WHEN id = ? THEN 1 ELSE 0 END, updated_at = ?`, st.ID, now); err != nil {
			return nil, err
		}
	}
	if patch.Terminal != nil {
		if *patch.Terminal {
			if _, err := db.Exec(`UPDATE card_states SET is_terminal = CASE WHEN id = ? THEN 1 ELSE 0 END, updated_at = ?`, st.ID, now); err != nil {
				return nil, err
			}
		} else {
			if _, err := db.Exec(`UPDATE card_states SET is_terminal = 0, updated_at = ? WHERE id = ?`, now, st.ID); err != nil {
				return nil, err
			}
		}
	}
	return states(db)
}

// ReorderStates sets the full order by name. Every state must be named once.
func ReorderStates(p *project.Project, names []string) ([]State, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sts, err := states(db)
	if err != nil {
		return nil, err
	}
	if len(names) != len(sts) {
		return nil, fmt.Errorf("name all %d states in order", len(sts))
	}
	seen := map[string]bool{}
	now := store.Now()
	for i, n := range names {
		st, err := findState(sts, n)
		if err != nil {
			return nil, err
		}
		if seen[st.ID] {
			return nil, fmt.Errorf("%q named twice", n)
		}
		seen[st.ID] = true
		if _, err := db.Exec(`UPDATE card_states SET position = ?, updated_at = ? WHERE id = ?`, i, now, st.ID); err != nil {
			return nil, err
		}
	}
	return states(db)
}

// RemoveState deletes an empty, non-default state.
func RemoveState(p *project.Project, name string) ([]State, error) {
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sts, err := states(db)
	if err != nil {
		return nil, err
	}
	st, err := findState(sts, name)
	if err != nil {
		return nil, err
	}
	if st.Default {
		return nil, fmt.Errorf("%s is the default state; make another state default first", st.Name)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cards WHERE state_id = ? AND deleted_at = ''`, st.ID).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, fmt.Errorf("%s holds %d card(s); move them first", st.Name, n)
	}
	if _, err := db.Exec(`DELETE FROM card_states WHERE id = ?`, st.ID); err != nil {
		return nil, err
	}
	if err := tombstone(db, "card_states", st.ID); err != nil {
		return nil, err
	}
	return states(db)
}

// ------------------------------------------------------------------- new

// Input is what a caller supplies to New.
type Input struct {
	Type     string
	Title    string
	Body     string
	CardData map[string]any
	Size     string
	Priority string // P0..P3 or 0..3; empty means P0
	Tags     []string
	Assignee string
	Parent   string // card key
	DueAt    string // RFC3339 or YYYY-MM-DD
	Estimate int
	Artifact string
	Source   string // agent | user
}

// New mints a card in the default state. The key is <prefix>-<n>; numbers
// never reuse. Refused unless the type's top line and acceptance are there.
func New(p *project.Project, in Input, agent string) (*Card, error) {
	agent = agentOr(agent)
	typ, err := NormalizeType(in.Type)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("title is empty")
	}
	size, err := NormalizeSize(in.Size)
	if err != nil {
		return nil, err
	}
	pri := 0
	if strings.TrimSpace(in.Priority) != "" {
		if pri, err = NormalizePriority(in.Priority); err != nil {
			return nil, err
		}
	}
	due, err := parseDue(in.DueAt)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	for k, v := range in.CardData {
		data[k] = v
	}
	if err := Validate(data); err != nil {
		return nil, err
	}
	for _, k := range Seeded(typ) {
		if _, ok := data[k]; !ok {
			data[k] = Empty(k)
		}
	}
	if typ == "spike" {
		if sp, ok := data[SecSpike].(map[string]any); ok {
			if q, _ := sp["question"].(string); strings.TrimSpace(q) == "" {
				sp["question"] = title
			}
		}
	}
	if err := Check(typ, data); err != nil {
		return nil, err
	}
	src := strings.ToLower(strings.TrimSpace(in.Source))
	if src != "user" {
		src = "agent"
	}
	data[SecSource] = src

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
	parentID := ""
	if strings.TrimSpace(in.Parent) != "" {
		par, err := get(db, p, in.Parent)
		if err != nil {
			return nil, fmt.Errorf("parent: %w", err)
		}
		parentID = par.ID
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
	var maxPos sql.NullInt64
	_ = tx.QueryRow(`SELECT MAX(position) FROM cards WHERE state_id = ? AND deleted_at = ''`, def.ID).Scan(&maxPos)
	pos := int(maxPos.Int64) + 1
	raw, _ := json.Marshal(data)
	now := store.Now()
	id := project.NewID()
	if _, err := tx.Exec(`INSERT INTO cards (id, number, key, title, body, type, state_id, size, priority, position, card_data, assignee, parent_id, due_at, estimate_minutes, artifact, external_key, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
		id, num, p.Prefix+"-"+strconv.Itoa(num), title, strings.TrimSpace(in.Body), typ, def.ID, size, pri, pos, string(raw),
		strings.TrimSpace(in.Assignee), parentID, due, in.Estimate, strings.TrimSpace(in.Artifact), agent, now, now); err != nil {
		return nil, err
	}
	for _, t := range normTags(in.Tags) {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO card_tags (card_id, tag) VALUES (?, ?)`, id, t); err != nil {
			return nil, err
		}
	}
	if err := logEvent(tx, id, agent, "created", typ+" in "+def.Name); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return get(db, p, id)
}

func parseDue(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("due %q: use YYYY-MM-DD or RFC3339", s)
}

func normTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = project.Slugify(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ------------------------------------------------------------------ read

const selectCols = `SELECT c.id, c.number, c.key, c.title, c.body, c.type, c.state_id, s.name, c.size, c.priority, c.position, c.card_data,
	c.assignee, COALESCE(par.key, ''), c.due_at, c.estimate_minutes, c.artifact, c.external_key, c.created_by, c.created_at, c.updated_at, c.completed_at
	FROM cards c JOIN card_states s ON s.id = c.state_id LEFT JOIN cards par ON par.id = c.parent_id WHERE c.deleted_at = ''`

func scan(rows interface{ Scan(...any) error }) (*Card, error) {
	var c Card
	var raw, due, created, updated, completed string
	if err := rows.Scan(&c.ID, &c.Number, &c.Key, &c.Title, &c.Body, &c.Type, &c.StateID, &c.State, &c.Size, &c.Priority, &c.Position, &raw,
		&c.Assignee, &c.ParentKey, &due, &c.Estimate, &c.Artifact, &c.External, &c.CreatedBy, &created, &updated, &completed); err != nil {
		return nil, err
	}
	c.CardData = map[string]any{}
	_ = json.Unmarshal([]byte(raw), &c.CardData)
	c.CreatedAt, c.UpdatedAt = store.Parse(created), store.Parse(updated)
	if due != "" {
		c.DueAt = store.Parse(due)
	}
	if completed != "" {
		c.CompletedAt = store.Parse(completed)
	}
	return &c, nil
}

func loadTags(db *sql.DB, cards []Card) error {
	if len(cards) == 0 {
		return nil
	}
	rows, err := db.Query(`SELECT card_id, tag FROM card_tags ORDER BY tag`)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[string][]string{}
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return err
		}
		byID[id] = append(byID[id], tag)
	}
	for i := range cards {
		cards[i].Tags = byID[cards[i].ID]
	}
	return rows.Err()
}

// Filter narrows a listing. Completed cards are hidden unless All or the
// terminal state is named.
type Filter struct {
	State    string
	Type     string
	Tag      string
	Assignee string
	Parent   string
	Query    string // substring of key, title, body
	All      bool
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
	return list(db, p, f)
}

func list(db *sql.DB, p *project.Project, f Filter) ([]Card, error) {
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
	if f.Tag != "" {
		q += ` AND c.id IN (SELECT card_id FROM card_tags WHERE tag = ?)`
		args = append(args, project.Slugify(f.Tag))
	}
	if f.Assignee != "" {
		q += ` AND c.assignee = ?`
		args = append(args, f.Assignee)
	}
	if f.Parent != "" {
		par, err := get(db, p, f.Parent)
		if err != nil {
			return nil, err
		}
		q += ` AND c.parent_id = ?`
		args = append(args, par.ID)
	}
	if f.Query != "" {
		like := "%" + f.Query + "%"
		q += ` AND (c.key LIKE ? OR c.title LIKE ? OR c.body LIKE ? OR c.card_data LIKE ?)`
		args = append(args, like, like, like, like)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, loadTags(db, out)
}

// Get finds a card by key (GRS-14, case-insensitive), bare number, id, or id prefix.
func Get(p *project.Project, key string) (*Card, error) {
	if !store.Exists(p) {
		return nil, fmt.Errorf("no card %q", key)
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return get(db, p, key)
}

func get(db *sql.DB, p *project.Project, key string) (*Card, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("card key is empty")
	}
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
	list := []Card{*hit}
	if err := loadTags(db, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// Exists reports whether a key names a card; runs and desk refs use it.
func Exists(p *project.Project, key string) bool {
	_, err := Get(p, key)
	return err == nil
}

// Full is a card with everything hanging off it.
type Full struct {
	Card
	Run        *RunRef
	Comments   []Comment
	Links      []Link
	Checklists []Checklist
	Children   []Card
	Events     []Event
}

// Show loads the card and its trail.
func Show(p *project.Project, key string) (*Full, error) {
	if !store.Exists(p) {
		return nil, fmt.Errorf("no card %q", key)
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	f := &Full{Card: *c, Run: runRef(db, c.ID)}
	if f.Comments, err = comments(db, c); err != nil {
		return nil, err
	}
	if f.Links, err = links(db, c); err != nil {
		return nil, err
	}
	if f.Checklists, err = checklists(db, c); err != nil {
		return nil, err
	}
	if f.Children, err = list(db, p, Filter{Parent: c.Key, All: true}); err != nil {
		return nil, err
	}
	if f.Events, err = events(db, c, 50); err != nil {
		return nil, err
	}
	return f, nil
}

// ---------------------------------------------------------------- update

// Patch is a partial update; nil pointers leave a field alone. Sections are
// merged key by key: sent keys replace, omitted keys stay. The result must
// still pass Check.
type Patch struct {
	Title    *string
	Body     *string
	Type     *string
	Size     *string
	Priority *string
	Assignee *string
	Parent   *string // card key; empty clears
	DueAt    *string // empty clears
	Estimate *int
	Artifact *string
	External *string
	CardData map[string]any
}

// Update applies a patch, re-checks the shape, and logs what changed.
func Update(p *project.Project, key string, patch Patch, agent string) (*Card, error) {
	agent = agentOr(agent)
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	var changed []string
	if patch.Title != nil && strings.TrimSpace(*patch.Title) != c.Title {
		if strings.TrimSpace(*patch.Title) == "" {
			return nil, fmt.Errorf("title is empty")
		}
		c.Title = strings.TrimSpace(*patch.Title)
		changed = append(changed, "title")
	}
	if patch.Body != nil {
		c.Body = strings.TrimSpace(*patch.Body)
		changed = append(changed, "body")
	}
	if patch.Type != nil {
		t, err := NormalizeType(*patch.Type)
		if err != nil {
			return nil, err
		}
		if t != c.Type {
			c.Type = t
			changed = append(changed, "type="+t)
		}
	}
	if patch.Size != nil {
		s, err := NormalizeSize(*patch.Size)
		if err != nil {
			return nil, err
		}
		c.Size = s
		changed = append(changed, "size="+orDash(s))
	}
	if patch.Priority != nil {
		pr, err := NormalizePriority(*patch.Priority)
		if err != nil {
			return nil, err
		}
		c.Priority = pr
		changed = append(changed, "P"+strconv.Itoa(pr))
	}
	if patch.Assignee != nil {
		c.Assignee = strings.TrimSpace(*patch.Assignee)
		changed = append(changed, "assignee="+orDash(c.Assignee))
	}
	parentID := sql.NullString{}
	parentSet := false
	if patch.Parent != nil {
		parentSet = true
		if strings.TrimSpace(*patch.Parent) != "" {
			par, err := get(db, p, *patch.Parent)
			if err != nil {
				return nil, fmt.Errorf("parent: %w", err)
			}
			if par.ID == c.ID {
				return nil, fmt.Errorf("a card cannot be its own parent")
			}
			parentID = sql.NullString{String: par.ID, Valid: true}
			c.ParentKey = par.Key
		} else {
			c.ParentKey = ""
		}
		changed = append(changed, "parent="+orDash(c.ParentKey))
	}
	due := ""
	dueSet := false
	if patch.DueAt != nil {
		dueSet = true
		if due, err = parseDue(*patch.DueAt); err != nil {
			return nil, err
		}
		changed = append(changed, "due="+orDash(due))
	}
	if patch.Estimate != nil {
		c.Estimate = *patch.Estimate
		changed = append(changed, "estimate="+strconv.Itoa(c.Estimate)+"m")
	}
	if patch.Artifact != nil {
		c.Artifact = strings.TrimSpace(*patch.Artifact)
		changed = append(changed, "artifact="+orDash(c.Artifact))
	}
	if patch.External != nil {
		c.External = strings.TrimSpace(*patch.External)
		changed = append(changed, "external="+orDash(c.External))
	}
	if len(patch.CardData) > 0 {
		if err := Validate(patch.CardData); err != nil {
			return nil, err
		}
		var secs []string
		for k, v := range patch.CardData {
			c.CardData[k] = v
			secs = append(secs, k)
		}
		changed = append(changed, "sections: "+strings.Join(secs, ", "))
	}
	for _, k := range Seeded(c.Type) {
		if _, ok := c.CardData[k]; !ok {
			c.CardData[k] = Empty(k)
		}
	}
	if err := Check(c.Type, c.CardData); err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return c, nil
	}
	raw, _ := json.Marshal(c.CardData)
	now := store.Now()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE cards SET title = ?, body = ?, type = ?, size = ?, priority = ?, assignee = ?, estimate_minutes = ?, artifact = ?, external_key = ?, card_data = ?, updated_at = ?, updated_by = ? WHERE id = ?`,
		c.Title, c.Body, c.Type, c.Size, c.Priority, c.Assignee, c.Estimate, c.Artifact, c.External, string(raw), now, agent, c.ID); err != nil {
		return nil, err
	}
	if parentSet {
		pid := ""
		if parentID.Valid {
			pid = parentID.String
		}
		if _, err := tx.Exec(`UPDATE cards SET parent_id = ? WHERE id = ?`, pid, c.ID); err != nil {
			return nil, err
		}
	}
	if dueSet {
		if _, err := tx.Exec(`UPDATE cards SET due_at = ? WHERE id = ?`, due, c.ID); err != nil {
			return nil, err
		}
	}
	if err := logEvent(tx, c.ID, agent, "updated", strings.Join(changed, "; ")); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return get(db, p, c.ID)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ActiveRun returns the id and agent of the run that currently owns a card,
// or empty strings. The runs table is written by package runbook; reading
// it here keeps the lock in one place without an import cycle.
func ActiveRun(p *project.Project, cardID string) (runID, agent string) {
	if !store.Exists(p) {
		return "", ""
	}
	db, err := store.Open(p)
	if err != nil {
		return "", ""
	}
	defer db.Close()
	return activeRun(db, cardID)
}

func activeRun(db *sql.DB, cardID string) (runID, agent string) {
	_ = db.QueryRow(`SELECT id, agent FROM runs WHERE card_id = ? AND status = 'active' ORDER BY started_at DESC LIMIT 1`, cardID).Scan(&runID, &agent)
	return runID, agent
}

// RunRef is the active run shown on a card.
type RunRef struct {
	ID      string
	Runbook string
	Agent   string
	Step    string
}

func runRef(db *sql.DB, cardID string) *RunRef {
	var ref RunRef
	err := db.QueryRow(`SELECT r.id, r.runbook, r.agent, COALESCE((SELECT s.idx || '/' || (SELECT COUNT(*) FROM run_steps WHERE run_id = r.id) || ' ' || s.text FROM run_steps s WHERE s.run_id = r.id AND s.state = 'current'), '')
		FROM runs r WHERE r.card_id = ? AND r.status = 'active' ORDER BY r.started_at DESC LIMIT 1`, cardID).Scan(&ref.ID, &ref.Runbook, &ref.Agent, &ref.Step)
	if err != nil {
		return nil
	}
	return &ref
}

// Note appends a line to a card's activity from outside the package (runs).
func Note(p *project.Project, cardID, agent, kind, detail string) error {
	db, err := store.Open(p)
	if err != nil {
		return err
	}
	defer db.Close()
	return logEvent(db, cardID, agentOr(agent), kind, detail)
}

// Move puts a card in a state at a position: "top", "bottom" (default), or
// "after:<key>". Entering the terminal state stamps completed_at; leaving it
// clears the stamp. A WIP limit on the target is a warning in the event,
// not a refusal: agents should see it, humans decide. A card with an active
// run is refused: the run drives it.
func Move(p *project.Project, key, state, position, agent string) (*Card, string, error) {
	return move(p, key, state, position, agent, false)
}

// MoveForRun is Move for the run that owns the card.
func MoveForRun(p *project.Project, key, state, agent string) (*Card, string, error) {
	return move(p, key, state, "bottom", agent, true)
}

func move(p *project.Project, key, state, position, agent string, byRun bool) (*Card, string, error) {
	agent = agentOr(agent)
	db, err := store.Open(p)
	if err != nil {
		return nil, "", err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, "", err
	}
	if !byRun {
		if id, who := activeRun(db, c.ID); id != "" {
			return nil, "", fmt.Errorf("%s is driven by run %s (%s); the runbook's steps move it, or abandon the run", c.Key, id[:8], who)
		}
	}
	sts, err := states(db)
	if err != nil {
		return nil, "", err
	}
	var st *State
	if strings.TrimSpace(state) == "" {
		st, _ = findState(sts, c.State)
	} else if st, err = findState(sts, state); err != nil {
		return nil, "", err
	}
	// Position within the target.
	var pos int
	position = strings.TrimSpace(position)
	switch {
	case position == "" || position == "bottom":
		var maxPos sql.NullInt64
		_ = db.QueryRow(`SELECT MAX(position) FROM cards WHERE state_id = ? AND deleted_at = '' AND id <> ?`, st.ID, c.ID).Scan(&maxPos)
		pos = int(maxPos.Int64) + 1
	case position == "top":
		pos = 0
	case strings.HasPrefix(position, "after:"):
		after, err := get(db, p, strings.TrimPrefix(position, "after:"))
		if err != nil {
			return nil, "", err
		}
		if after.StateID != st.ID {
			return nil, "", fmt.Errorf("%s is in %s, not %s", after.Key, after.State, st.Name)
		}
		pos = after.Position + 1
	default:
		return nil, "", fmt.Errorf("position %q; one of top, bottom, after:<key>", position)
	}
	warn := ""
	if st.WIPLimit > 0 && st.ID != c.StateID {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM cards WHERE state_id = ? AND deleted_at = ''`, st.ID).Scan(&n)
		if n+1 > st.WIPLimit {
			warn = fmt.Sprintf("%s is over its WIP limit (%d of %d)", st.Name, n+1, st.WIPLimit)
		}
	}
	now := store.Now()
	completed := ""
	if st.Terminal {
		completed = now
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE cards SET position = position + 1 WHERE state_id = ? AND position >= ? AND deleted_at = '' AND id <> ?`, st.ID, pos, c.ID); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(`UPDATE cards SET state_id = ?, position = ?, completed_at = ?, updated_at = ?, updated_by = ? WHERE id = ?`, st.ID, pos, completed, now, agent, c.ID); err != nil {
		return nil, "", err
	}
	detail := c.State + " -> " + st.Name
	if st.ID == c.StateID {
		detail = "reordered in " + st.Name + " (" + position + ")"
	}
	if warn != "" {
		detail += "; " + warn
	}
	if err := logEvent(tx, c.ID, agent, "moved", detail); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	out, err := get(db, p, c.ID)
	return out, warn, err
}

// Claim assigns the card to the agent and moves it to the first state after
// the default one, unless it is already past it. One verb for "I'm on it".
func Claim(p *project.Project, key, agent string) (*Card, string, error) {
	agent = agentOr(agent)
	if err := claimable(p, key, agent); err != nil {
		return nil, "", err
	}
	c, err := Update(p, key, Patch{Assignee: &agent}, agent)
	if err != nil {
		return nil, "", err
	}
	sts, err := States(p)
	if err != nil {
		return nil, "", err
	}
	defIdx, curIdx := 0, 0
	for i, s := range sts {
		if s.Default {
			defIdx = i
		}
		if s.ID == c.StateID {
			curIdx = i
		}
	}
	if curIdx > defIdx || defIdx+1 >= len(sts) || sts[defIdx+1].Terminal {
		return c, "", nil
	}
	return Move(p, c.Key, sts[defIdx+1].Name, "bottom", agent)
}

// claimable refuses a claim on a card another agent's run owns.
func claimable(p *project.Project, key, agent string) error {
	c, err := Get(p, key)
	if err != nil {
		return err
	}
	if id, who := ActiveRun(p, c.ID); id != "" && who != agent {
		return fmt.Errorf("%s is driven by run %s (%s)", c.Key, id[:8], who)
	}
	return nil
}

// Delete soft-deletes a card. The number is never reused; children are
// detached, not deleted.
func Delete(p *project.Project, key, agent string) (*Card, error) {
	agent = agentOr(agent)
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	if id, who := activeRun(db, c.ID); id != "" {
		return nil, fmt.Errorf("%s is driven by run %s (%s); abandon the run first", c.Key, id[:8], who)
	}
	now := store.Now()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE cards SET deleted_at = ?, updated_at = ?, updated_by = ? WHERE id = ?`, now, now, agent, c.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE cards SET parent_id = '' WHERE parent_id = ?`, c.ID); err != nil {
		return nil, err
	}
	if err := logEvent(tx, c.ID, agent, "deleted", c.Title); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}

// -------------------------------------------------------------- comments

// Comment adds evidence to a card. Logs, traces and findings go here, not in
// the body.
func AddComment(p *project.Project, key, body, agent string) (*Comment, error) {
	agent = agentOr(agent)
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("comment is empty")
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	now := store.Now()
	cm := &Comment{ID: project.NewID(), CardKey: c.Key, Body: body, Agent: agent, CreatedAt: store.Parse(now)}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO card_comments (id, card_id, body, agent, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, cm.ID, c.ID, body, agent, now, now); err != nil {
		return nil, err
	}
	if err := logEvent(tx, c.ID, agent, "commented", firstLine(body)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return cm, nil
}

func comments(db *sql.DB, c *Card) ([]Comment, error) {
	rows, err := db.Query(`SELECT id, body, agent, created_at FROM card_comments WHERE card_id = ? AND deleted_at = '' ORDER BY created_at, id`, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var cm Comment
		var at string
		if err := rows.Scan(&cm.ID, &cm.Body, &cm.Agent, &at); err != nil {
			return nil, err
		}
		cm.CardKey, cm.CreatedAt = c.Key, store.Parse(at)
		out = append(out, cm)
	}
	return out, rows.Err()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// ------------------------------------------------------------------ tags

// Tag adds and removes tags. Tags are slugs; a tag names a slice of the
// board (a module, a customer, a theme), not a status.
func Tag(p *project.Project, key string, add, remove []string, agent string) (*Card, error) {
	agent = agentOr(agent)
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var detail []string
	for _, t := range normTags(add) {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO card_tags (card_id, tag) VALUES (?, ?)`, c.ID, t); err != nil {
			return nil, err
		}
		detail = append(detail, "+"+t)
	}
	for _, t := range normTags(remove) {
		if _, err := tx.Exec(`DELETE FROM card_tags WHERE card_id = ? AND tag = ?`, c.ID, t); err != nil {
			return nil, err
		}
		detail = append(detail, "-"+t)
	}
	if len(detail) == 0 {
		return c, nil
	}
	if _, err := tx.Exec(`UPDATE cards SET updated_at = ?, updated_by = ? WHERE id = ?`, store.Now(), agent, c.ID); err != nil {
		return nil, err
	}
	if err := logEvent(tx, c.ID, agent, "tagged", strings.Join(detail, " ")); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return get(db, p, c.ID)
}

// Tags lists every tag in use with its count.
func Tags(p *project.Project) (map[string]int, error) {
	out := map[string]int{}
	if !store.Exists(p) {
		return out, nil
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT t.tag, COUNT(*) FROM card_tags t JOIN cards c ON c.id = t.card_id WHERE c.deleted_at = '' GROUP BY t.tag ORDER BY t.tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

// ----------------------------------------------------------------- links

// AddLink records that this card blocks another, relates to another, or
// references a URL. "blocked by X" is written as X blocks this card.
func AddLink(p *project.Project, key, kind, target, url, title, agent string) (*Link, error) {
	agent = agentOr(agent)
	kind = strings.ToLower(strings.TrimSpace(kind))
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	l := &Link{ID: project.NewID(), CardKey: c.Key, Kind: kind, Agent: agent}
	targetID := ""
	switch kind {
	case "blocks", "related":
		t, err := get(db, p, target)
		if err != nil {
			return nil, fmt.Errorf("target: %w", err)
		}
		if t.ID == c.ID {
			return nil, fmt.Errorf("a card cannot link to itself")
		}
		targetID, l.Target = t.ID, t.Key
	case "blocked-by":
		// Store as target blocks this card.
		t, err := get(db, p, target)
		if err != nil {
			return nil, fmt.Errorf("target: %w", err)
		}
		if t.ID == c.ID {
			return nil, fmt.Errorf("a card cannot block itself")
		}
		c, t = t, c
		kind, targetID, l.Kind, l.CardKey, l.Target = "blocks", t.ID, "blocks", c.Key, t.Key
	case "ref":
		url = strings.TrimSpace(url)
		if url == "" {
			return nil, fmt.Errorf("ref needs a url")
		}
		l.URL, l.Title = url, strings.TrimSpace(title)
	default:
		return nil, fmt.Errorf("link kind %q; one of blocks, blocked-by, related, ref", kind)
	}
	now := store.Now()
	l.CreatedAt = store.Parse(now)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO card_links (id, card_id, kind, target_card_id, url, title, agent, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, c.ID, kind, targetID, l.URL, l.Title, agent, now, now); err != nil {
		return nil, err
	}
	detail := kind + " " + l.Target + l.URL
	if err := logEvent(tx, c.ID, agent, "linked", detail); err != nil {
		return nil, err
	}
	if targetID != "" {
		if err := logEvent(tx, targetID, agent, "linked", "blocked-by/related from "+c.Key); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return l, nil
}

// RemoveLink deletes a link by id (or prefix) from either side.
func RemoveLink(p *project.Project, key, linkID, agent string) error {
	agent = agentOr(agent)
	db, err := store.Open(p)
	if err != nil {
		return err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT id FROM card_links WHERE (card_id = ? OR target_card_id = ?) AND id LIKE ?`, c.ID, c.ID, strings.TrimSpace(linkID)+"%")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return fmt.Errorf("no link %q on %s", linkID, c.Key)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM card_links WHERE id = ?`, id); err != nil {
			return err
		}
		if err := tombstone(tx, "card_links", id); err != nil {
			return err
		}
	}
	if err := logEvent(tx, c.ID, agent, "linked", "removed "+linkID); err != nil {
		return err
	}
	return tx.Commit()
}

// links returns outgoing links plus incoming blocks (as blocked-by) and
// incoming related.
func links(db *sql.DB, c *Card) ([]Link, error) {
	rows, err := db.Query(`SELECT l.id, l.kind, l.card_id, COALESCE(src.key, ''), COALESCE(tgt.key, ''), l.url, l.title, l.agent, l.created_at
		FROM card_links l LEFT JOIN cards src ON src.id = l.card_id LEFT JOIN cards tgt ON tgt.id = l.target_card_id
		WHERE l.card_id = ? OR l.target_card_id = ? ORDER BY l.created_at`, c.ID, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var srcID, srcKey, at string
		if err := rows.Scan(&l.ID, &l.Kind, &srcID, &srcKey, &l.Target, &l.URL, &l.Title, &l.Agent, &at); err != nil {
			return nil, err
		}
		l.CardKey, l.CreatedAt = c.Key, store.Parse(at)
		if srcID != c.ID {
			// Incoming: flip the view.
			if l.Kind == "blocks" {
				l.Kind = "blocked-by"
			}
			l.Target = srcKey
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Blocked reports open cards that block this one.
func (f *Full) Blocked() []string {
	var out []string
	for _, l := range f.Links {
		if l.Kind == "blocked-by" {
			out = append(out, l.Target)
		}
	}
	return out
}

// ------------------------------------------------------------ checklists

// AddChecklist creates a checklist, e.g. UAT, with optional initial items.
func AddChecklist(p *project.Project, key, title string, items []string, agent string) (*Checklist, error) {
	agent = agentOr(agent)
	title = strings.TrimSpace(title)
	if title == "" {
		title = "UAT"
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM card_checklists WHERE card_id = ?`, c.ID).Scan(&n)
	now := store.Now()
	cl := &Checklist{ID: project.NewID(), CardKey: c.Key, Title: title, Position: n}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO card_checklists (id, card_id, title, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, cl.ID, c.ID, title, n, now, now); err != nil {
		return nil, err
	}
	for i, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		id := project.NewID()
		if _, err := tx.Exec(`INSERT INTO card_checklist_items (id, checklist_id, title, done, position, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?, ?)`, id, cl.ID, it, i, now, now); err != nil {
			return nil, err
		}
		cl.Items = append(cl.Items, ChecklistItem{ID: id, Title: it, Position: i})
	}
	if err := logEvent(tx, c.ID, agent, "checklist", "added "+title+" ("+strconv.Itoa(len(cl.Items))+" items)"); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return cl, nil
}

func findChecklist(db *sql.DB, c *Card, ref string) (*Checklist, error) {
	lists, err := checklists(db, c)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" && len(lists) == 1 {
		return &lists[0], nil
	}
	for i := range lists {
		if strings.EqualFold(lists[i].Title, ref) || lists[i].ID == ref || (ref != "" && strings.HasPrefix(lists[i].ID, ref)) {
			return &lists[i], nil
		}
	}
	if ref == "" {
		return nil, fmt.Errorf("%s has %d checklists; name one", c.Key, len(lists))
	}
	return nil, fmt.Errorf("no checklist %q on %s", ref, c.Key)
}

// AddItem appends an item to a checklist.
func AddItem(p *project.Project, key, checklist, title, agent string) (*Checklist, error) {
	agent = agentOr(agent)
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("item is empty")
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	cl, err := findChecklist(db, c, checklist)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := store.Now()
	if _, err := tx.Exec(`INSERT INTO card_checklist_items (id, checklist_id, title, done, position, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?, ?)`,
		project.NewID(), cl.ID, title, len(cl.Items), now, now); err != nil {
		return nil, err
	}
	if err := logEvent(tx, c.ID, agent, "checklist", cl.Title+": + "+title); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return findChecklist(db, c, cl.ID)
}

// Tick marks an item done or not. item is a title, an id prefix, or a
// 1-based index.
func Tick(p *project.Project, key, checklist, item string, done bool, agent string) (*Checklist, error) {
	agent = agentOr(agent)
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return nil, err
	}
	cl, err := findChecklist(db, c, checklist)
	if err != nil {
		return nil, err
	}
	var hit *ChecklistItem
	item = strings.TrimSpace(item)
	if n, err := strconv.Atoi(item); err == nil && n >= 1 && n <= len(cl.Items) {
		hit = &cl.Items[n-1]
	}
	for i := range cl.Items {
		if hit != nil {
			break
		}
		if strings.EqualFold(cl.Items[i].Title, item) || cl.Items[i].ID == item || strings.HasPrefix(cl.Items[i].ID, item) {
			hit = &cl.Items[i]
		}
	}
	if hit == nil {
		return nil, fmt.Errorf("no item %q in %s", item, cl.Title)
	}
	now := store.Now()
	doneAt, doneBy := "", ""
	if done {
		doneAt, doneBy = now, agent
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE card_checklist_items SET done = ?, done_at = ?, done_by = ?, updated_at = ? WHERE id = ?`, b2i(done), doneAt, doneBy, now, hit.ID); err != nil {
		return nil, err
	}
	mark := "[x] "
	if !done {
		mark = "[ ] "
	}
	if err := logEvent(tx, c.ID, agent, "checklist", cl.Title+": "+mark+hit.Title); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return findChecklist(db, c, cl.ID)
}

// RemoveChecklist deletes a checklist and its items.
func RemoveChecklist(p *project.Project, key, checklist, agent string) error {
	agent = agentOr(agent)
	db, err := store.Open(p)
	if err != nil {
		return err
	}
	defer db.Close()
	c, err := get(db, p, key)
	if err != nil {
		return err
	}
	cl, err := findChecklist(db, c, checklist)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM card_checklist_items WHERE checklist_id = ?`, cl.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM card_checklists WHERE id = ?`, cl.ID); err != nil {
		return err
	}
	// The API deletes the items with their checklist.
	if err := tombstone(tx, "card_checklists", cl.ID); err != nil {
		return err
	}
	if err := logEvent(tx, c.ID, agent, "checklist", "removed "+cl.Title); err != nil {
		return err
	}
	return tx.Commit()
}

func checklists(db *sql.DB, c *Card) ([]Checklist, error) {
	rows, err := db.Query(`SELECT id, title, position FROM card_checklists WHERE card_id = ? ORDER BY position, created_at`, c.ID)
	if err != nil {
		return nil, err
	}
	var out []Checklist
	for rows.Next() {
		var cl Checklist
		if err := rows.Scan(&cl.ID, &cl.Title, &cl.Position); err != nil {
			rows.Close()
			return nil, err
		}
		cl.CardKey = c.Key
		out = append(out, cl)
	}
	rows.Close()
	for i := range out {
		irows, err := db.Query(`SELECT id, title, done, position, done_at, done_by FROM card_checklist_items WHERE checklist_id = ? ORDER BY position, created_at`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for irows.Next() {
			var it ChecklistItem
			var d int
			var at string
			if err := irows.Scan(&it.ID, &it.Title, &d, &it.Position, &at, &it.DoneBy); err != nil {
				irows.Close()
				return nil, err
			}
			it.Done = d == 1
			if at != "" {
				it.DoneAt = store.Parse(at)
			}
			out[i].Items = append(out[i].Items, it)
		}
		irows.Close()
	}
	return out, nil
}

// ---------------------------------------------------------------- events

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func logEvent(tx execer, cardID, agent, kind, detail string) error {
	_, err := tx.Exec(`INSERT INTO card_events (card_id, at, agent, kind, detail, uid) VALUES (?, ?, ?, ?, ?, ?)`, cardID, store.Now(), agent, kind, detail, project.NewID())
	return err
}

// tombstone leaves a hard-deleted row's id for the next sync push to delete
// on the API (GRS-47). Cards and comments are soft-deleted and need none.
func tombstone(tx execer, tbl, id string) error {
	_, err := tx.Exec(`INSERT INTO sync_deletes (tbl, id, deleted_at) VALUES (?, ?, ?) ON CONFLICT(tbl, id) DO NOTHING`, tbl, id, store.Now())
	return err
}

func events(db *sql.DB, c *Card, limit int) ([]Event, error) {
	rows, err := db.Query(`SELECT id, at, agent, kind, detail FROM card_events WHERE card_id = ? ORDER BY at DESC, id DESC LIMIT ?`, c.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Agent, &e.Kind, &e.Detail); err != nil {
			return nil, err
		}
		e.At = store.Parse(at)
		out = append(out, e)
	}
	// Oldest first for reading.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Activity returns the latest events across the board, newest first.
func Activity(p *project.Project, limit int) ([]Event, map[int64]string, error) {
	keys := map[int64]string{}
	if !store.Exists(p) {
		return nil, keys, nil
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT e.id, e.at, e.agent, e.kind, e.detail, c.key FROM card_events e JOIN cards c ON c.id = e.card_id ORDER BY e.at DESC, e.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at, key string
		if err := rows.Scan(&e.ID, &at, &e.Agent, &e.Kind, &e.Detail, &key); err != nil {
			return nil, nil, err
		}
		e.At = store.Parse(at)
		keys[e.ID] = key
		out = append(out, e)
	}
	return out, keys, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
