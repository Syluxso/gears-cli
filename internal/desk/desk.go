// Package desk is an agent's inbox and workspace: items placed by the user,
// by other agents, and by Gears itself. Type is lifecycle; there is no
// separate state machine per item beyond open and closed, and how an item
// closes depends on its type.
package desk

import (
	"fmt"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/session"
	"github.com/Syluxso/gears-cli/internal/store"
)

// Types and how each one closes.
var Types = []string{"info", "ask", "event", "work", "handoff"}

// Closer names the verb that closes a type. handoff has none: it never dies.
var Closer = map[string]string{
	"info":  "read",
	"ask":   "answer",
	"event": "dismiss",
	"work":  "done",
}

// Item is one thing on a desk.
type Item struct {
	ID        string
	Type      string
	From      string
	To        string // empty means anyone in the tenant
	Text      string
	Ref       string // optional pointer: a card key, run id, artifact file
	CreatedAt time.Time
	State     string // open | closed
	ClosedAt  time.Time
	ClosedBy  string
	Answer    string
	// GRS-35: Author is the user who created the item (set by the API, ""
	// until the item has synced); AssignedTo the user it is for ("" nobody
	// in particular). From and To stay agent labels.
	Author     string
	AssignedTo string
}

// Short is the id shown in listings: the last eight digits. A v7 id's
// leading digits are its timestamp, so items sent together share a prefix.
func (i Item) Short() string { return i.ID[len(i.ID)-8:] }

func validType(t string) bool {
	for _, x := range Types {
		if x == t {
			return true
		}
	}
	return false
}

// Send places an item on a desk. from defaults to the current agent.
func Send(p *project.Project, typ, from, to, text, ref string) (*Item, error) {
	return SendAssigned(p, typ, from, to, text, ref, "")
}

// SendAssigned is Send with the user the item is for (GRS-35): an account
// id, "" for nobody in particular. The author is the API's to set.
func SendAssigned(p *project.Project, typ, from, to, text, ref, assignedTo string) (*Item, error) {
	typ = strings.ToLower(strings.TrimSpace(typ))
	if !validType(typ) {
		return nil, fmt.Errorf("type %q; one of %s", typ, strings.Join(Types, ", "))
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("item text is empty")
	}
	if from == "" {
		from = session.DefaultAgent()
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	it := &Item{ID: project.NewID(), Type: typ, From: from, To: strings.TrimSpace(to), Text: text, Ref: ref, State: "open", AssignedTo: strings.TrimSpace(assignedTo)}
	now := store.Now()
	if _, err := db.Exec(`INSERT INTO desk_items (id, type, from_agent, to_agent, text, ref, created_at, state, assigned_to)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
		it.ID, it.Type, it.From, it.To, it.Text, it.Ref, now, it.AssignedTo); err != nil {
		return nil, err
	}
	it.CreatedAt = store.Parse(now)
	return it, nil
}

// Filter narrows a listing.
type Filter struct {
	Agent string // show items addressed to this agent or to anyone
	All   bool   // include closed items
	Type  string
}

// List returns items, oldest first. Handoffs are always included while open.
func List(p *project.Project, f Filter) ([]Item, error) {
	if !store.Exists(p) {
		return nil, nil
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	q := `SELECT id, type, from_agent, to_agent, text, ref, created_at, state, closed_at, closed_by, answer, author, assigned_to FROM desk_items WHERE 1=1`
	var args []any
	if !f.All {
		q += ` AND state = 'open'`
	}
	if f.Agent != "" {
		q += ` AND (to_agent = '' OR to_agent = ?)`
		args = append(args, f.Agent)
	}
	if f.Type != "" {
		q += ` AND type = ?`
		args = append(args, f.Type)
	}
	q += ` ORDER BY created_at, id`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var created, closed string
		if err := rows.Scan(&it.ID, &it.Type, &it.From, &it.To, &it.Text, &it.Ref, &created, &it.State, &closed, &it.ClosedBy, &it.Answer, &it.Author, &it.AssignedTo); err != nil {
			return nil, err
		}
		it.CreatedAt = store.Parse(created)
		if closed != "" {
			it.ClosedAt = store.Parse(closed)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Get finds an item by full id or a unique prefix or suffix (listings show
// the last eight digits).
func Get(p *project.Project, key string) (*Item, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return nil, fmt.Errorf("no desk item id given")
	}
	all, err := List(p, Filter{All: true})
	if err != nil {
		return nil, err
	}
	var hit *Item
	for i := range all {
		if all[i].ID == key || strings.HasPrefix(all[i].ID, key) || strings.HasSuffix(all[i].ID, key) {
			if hit != nil {
				return nil, fmt.Errorf("id %q is ambiguous", key)
			}
			hit = &all[i]
		}
	}
	if hit == nil {
		return nil, fmt.Errorf("no desk item %q", key)
	}
	return hit, nil
}

// Exists reports whether an item id (or prefix) is on the desk; runs use it
// to verify "send a desk item" evidence.
func Exists(p *project.Project, key string) bool {
	_, err := Get(p, key)
	return err == nil
}

// Read returns an item and, if it is info, consumes it: info dies when read.
func Read(p *project.Project, key, agent string) (*Item, error) {
	it, err := Get(p, key)
	if err != nil {
		return nil, err
	}
	if it.Type == "info" && it.State == "open" {
		return close(p, it, agent, "")
	}
	return it, nil
}

// Answer closes an ask with a reply.
func Answer(p *project.Project, key, agent, answer string) (*Item, error) {
	it, err := Get(p, key)
	if err != nil {
		return nil, err
	}
	if it.Type != "ask" {
		return nil, fmt.Errorf("%s is %s, not ask; use: gears desk %s", it.Short(), it.Type, Closer[it.Type])
	}
	if strings.TrimSpace(answer) == "" {
		return nil, fmt.Errorf("an answer is required to close an ask")
	}
	return close(p, it, agent, answer)
}

// Done closes a work item.
func Done(p *project.Project, key, agent string) (*Item, error) {
	return closeAs(p, key, agent, "work")
}

// Dismiss closes an event without making it a card.
func Dismiss(p *project.Project, key, agent string) (*Item, error) {
	return closeAs(p, key, agent, "event")
}

// Clear closes open items of every type, handoffs included: with all, every
// open item in the workspace; otherwise only items addressed to agent.
func Clear(p *project.Project, agent string, all bool) ([]Item, error) {
	if agent == "" {
		agent = session.DefaultAgent()
	}
	open, err := List(p, Filter{})
	if err != nil {
		return nil, err
	}
	var closed []Item
	for i := range open {
		if !all && open[i].To != agent {
			continue
		}
		it, err := close(p, &open[i], agent, "")
		if err != nil {
			return closed, err
		}
		closed = append(closed, *it)
	}
	return closed, nil
}

func closeAs(p *project.Project, key, agent, want string) (*Item, error) {
	it, err := Get(p, key)
	if err != nil {
		return nil, err
	}
	if it.Type == "handoff" {
		return nil, fmt.Errorf("%s is a handoff; handoffs never close", it.Short())
	}
	if it.Type != want {
		return nil, fmt.Errorf("%s is %s, not %s; use: gears desk %s", it.Short(), it.Type, want, Closer[it.Type])
	}
	return close(p, it, agent, "")
}

func close(p *project.Project, it *Item, agent, answer string) (*Item, error) {
	if it.State == "closed" {
		return it, nil
	}
	if agent == "" {
		agent = session.DefaultAgent()
	}
	db, err := store.Open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	now := store.Now()
	if _, err := db.Exec(`UPDATE desk_items SET state = 'closed', closed_at = ?, closed_by = ?, answer = ? WHERE id = ?`,
		now, agent, answer, it.ID); err != nil {
		return nil, err
	}
	it.State, it.ClosedAt, it.ClosedBy, it.Answer = "closed", store.Parse(now), agent, answer
	return it, nil
}
