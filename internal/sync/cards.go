package sync

// Cards sync (GRS-47). The board is the workspace's; gears-api holds it.
// Rows go up in the API's push order (states, cards, comments, links,
// checklists, items, activity) with last-write-wins by updatedAt, and come
// down the same way. Two things the API decides and this cache adopts:
//
//   - Card numbers. A pushed number is a proposal; every card result says
//     the final number and key, and the local row (and card_sequence) take
//     them. A local card holding a number the API gave another card moves
//     to the next free local number until its own push settles it.
//   - State identity. States merge by name; a state the API knows under
//     another id is re-keyed here, and every card pointing at it follows.
//
// An API from before GRS-47 has no board route: cards are skipped with one
// note and stay local (their watermark does not move, so they go up on the
// first sync against an API that has the tables). Desk, sessions and
// artifacts sync either way.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

// cardTables is the API's push order for the board. Deletes run in reverse.
var cardTables = []string{"card_states", "cards", "card_comments", "card_links", "card_checklists", "card_checklist_items", "card_events"}

func cardRank(tbl string) int {
	for i, t := range cardTables {
		if t == tbl {
			return i + 1
		}
	}
	return 0
}

func isCardTable(tbl string) bool { return cardRank(tbl) > 0 }

// cardSync is one sync's view of the board.
type cardSync struct {
	ok     bool            // the API has card tables
	prefix string          // the workspace's key prefix on the API
	inSync map[string]bool // state ids whose local row is the API's row
	since  string          // cards_pushed_at before this sync
	now    string          // stamp taken as the card push starts
	sent   int             // card rows and deletes in this push
}

// prepareCards asks the API for the board. No board route: cards are
// skipped this sync. Otherwise states the API knows by name are re-keyed to
// its ids, and the local key prefix follows the workspace's.
func prepareCards(ctx context.Context, c *api.Client, p *project.Project, db *sql.DB, ws string, r *Report) (*cardSync, error) {
	cs := &cardSync{inSync: map[string]bool{}}
	b, err := c.Board(ctx, ws)
	if err != nil {
		r.Cards = "skipped"
		if errors.Is(err, api.ErrNoCards) {
			r.Notes = append(r.Notes, api.ErrNoCards.Error()+" (cards stay local until it is)")
		} else {
			r.Notes = append(r.Notes, "cards skipped: board: "+err.Error())
		}
		return cs, nil
	}
	cs.ok = true
	r.Cards = "synced"
	cs.prefix = strings.ToUpper(strings.TrimSpace(b.Prefix))
	if err := adoptStates(db, b.States, cs.inSync); err != nil {
		return nil, fmt.Errorf("card states: %w", err)
	}
	if err := adoptPrefix(p, db, cs.prefix, r); err != nil {
		return nil, err
	}
	cs.since, _ = stateDB(db, "cards_pushed_at")
	cs.now = store.Now()
	return cs, nil
}

// adoptPrefix makes local keys PREFIX-N with the API's prefix and saves it
// in the binding, so new local cards and bare-number lookups use it too.
func adoptPrefix(p *project.Project, db *sql.DB, prefix string, r *Report) error {
	if prefix == "" {
		return nil
	}
	res, err := db.Exec(`UPDATE cards SET key = ? || '-' || number WHERE key <> ? || '-' || number`, prefix, prefix)
	if err != nil {
		return fmt.Errorf("card keys: %w", err)
	}
	n, _ := res.RowsAffected()
	if p.Prefix != prefix {
		r.Notes = append(r.Notes, fmt.Sprintf("card prefix is the workspace's on the API: %s (was %s; %d key(s) rewritten)", prefix, p.Prefix, n))
		p.Prefix = prefix
		if err := p.Save(); err != nil {
			return err
		}
	}
	return nil
}

type localState struct {
	id, name, created, updated string
}

// adoptStates re-keys a local state to the API's id when the API holds a
// state of the same name under another id (the default states every cache
// seeds offline, mostly). A local row never edited since it was made, or
// older than the API's, takes the API's row; a newer local edit keeps its
// fields and goes up in the push. inSync collects the ids whose local row is
// already the API's, so the push does not resend them.
func adoptStates(db *sql.DB, states []api.BoardState, inSync map[string]bool) error {
	rows, err := db.Query(`SELECT id, name, created_at, updated_at FROM card_states`)
	if err != nil {
		return err
	}
	var local []localState
	for rows.Next() {
		var s localState
		if err := rows.Scan(&s.id, &s.name, &s.created, &s.updated); err != nil {
			rows.Close()
			return err
		}
		local = append(local, s)
	}
	rows.Close()
	for _, st := range states {
		upd := stamp(st.UpdatedAt)
		var byID, byName *localState
		for i := range local {
			if strings.EqualFold(local[i].id, st.ID) {
				byID = &local[i]
			} else if strings.EqualFold(local[i].name, st.Name) {
				byName = &local[i]
			}
		}
		if byID != nil {
			if byID.updated == upd {
				inSync[st.ID] = true
			}
			continue
		}
		if byName == nil {
			continue // pulled as a row
		}
		take := byName.updated == byName.created || upd >= byName.updated
		err := inTx(db, func(tx *sql.Tx) error {
			if err := remapState(tx, byName.id, st.ID); err != nil {
				return err
			}
			if !take {
				return nil
			}
			_, err := tx.Exec(`UPDATE card_states SET name = ?, position = ?, is_default = ?, is_terminal = ?, wip_limit = ?, created_at = ?, updated_at = ? WHERE id = ?`,
				st.Name, st.Position, b2i(st.IsDefault), b2i(st.IsTerminal), st.WIPLimit, stamp(st.CreatedAt), upd, st.ID)
			return err
		})
		if err != nil {
			return err
		}
		byName.id = st.ID
		if take {
			inSync[st.ID] = true
		}
	}
	return nil
}

// remapState moves a local state to another id: the row is re-keyed (or,
// when the target row is already here, merged into it) and every card that
// pointed at it follows. Foreign keys are checked at commit.
func remapState(tx *sql.Tx, from, to string) error {
	if from == to {
		return nil
	}
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM card_states WHERE id = ?`, to).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		if _, err := tx.Exec(`DELETE FROM card_states WHERE id = ?`, from); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE card_states SET id = ? WHERE id = ?`, to, from); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE cards SET state_id = ? WHERE state_id = ?`, to, from)
	return err
}

func inTx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ------------------------------------------------------------------ push

// collectCards adds the board's rows changed since the last card push, and
// the soft deletes of cards and comments, to in. Rows are read in full
// before the next query: the cache has one connection.
func collectCards(db *sql.DB, cs *cardSync, in *api.PushIn) error {
	since := cs.since
	add := func(tbl string, row map[string]any) {
		in.Rows[tbl] = append(in.Rows[tbl], row)
		cs.sent++
	}
	del := func(tbl, id string) {
		if in.Deletes == nil {
			in.Deletes = map[string][]string{}
		}
		in.Deletes[tbl] = append(in.Deletes[tbl], id)
		cs.sent++
	}

	// States.
	err := scanAll(db, `SELECT id, name, position, is_default, is_terminal, wip_limit, created_at, updated_at FROM card_states WHERE updated_at >= ?`,
		[]any{since}, func(sc func(...any) error) error {
			var id, name, created, updated string
			var pos, def, term, wip int
			if err := sc(&id, &name, &pos, &def, &term, &wip, &created, &updated); err != nil {
				return err
			}
			if cs.inSync[id] {
				return nil
			}
			add("card_states", map[string]any{"id": id, "name": name, "position": pos, "isDefault": def == 1, "isTerminal": term == 1,
				"wipLimit": wip, "createdAt": created, "updatedAt": updated})
			return nil
		})
	if err != nil {
		return err
	}

	// Cards. A deleted card goes up as its row and as a delete: rows land
	// first, so its trail reaches the API before the delete does.
	var cards []map[string]any
	err = scanAll(db, `SELECT c.id, c.number, c.type, c.title, c.body, c.card_data, c.state_id, COALESCE(s.name, ''), c.size, c.priority, c.position,
		c.assignee, c.parent_id, c.due_at, c.estimate_minutes, c.artifact, c.external_key, c.created_by, c.updated_by, c.created_at, c.updated_at,
		c.completed_at, c.deleted_at
		FROM cards c LEFT JOIN card_states s ON s.id = c.state_id WHERE c.updated_at >= ? OR (c.deleted_at <> '' AND c.deleted_at >= ?)`,
		[]any{since, since}, func(sc func(...any) error) error {
			var id, typ, title, body, data, stateID, state, size, assignee, parent, due, artifact, external, createdBy, updatedBy, created, updated, completed, deleted string
			var number, pri, pos, est int
			if err := sc(&id, &number, &typ, &title, &body, &data, &stateID, &state, &size, &pri, &pos, &assignee, &parent, &due, &est,
				&artifact, &external, &createdBy, &updatedBy, &created, &updated, &completed, &deleted); err != nil {
				return err
			}
			if updatedBy == "" {
				updatedBy = createdBy
			}
			var obj map[string]any
			if json.Unmarshal([]byte(data), &obj) != nil || obj == nil {
				data = "{}"
			}
			row := map[string]any{"id": id, "number": number, "type": typ, "title": title, "body": body, "cardData": json.RawMessage(data),
				"stateId": stateID, "state": state, "size": size, "priority": pri, "position": pos, "assigneeLabel": assignee,
				"parentId": nullStr(parent), "dueAt": nullStr(due), "estimateMinutes": est, "artifact": artifact, "externalKey": external,
				"tags": []string{}, "createdBy": createdBy, "updatedBy": updatedBy, "createdAt": created, "updatedAt": updated,
				"completedAt": nullStr(completed)}
			cards = append(cards, row)
			if deleted != "" && deleted >= since {
				del("cards", id)
			}
			return nil
		})
	if err != nil {
		return err
	}
	if len(cards) > 0 {
		tags := map[string][]string{}
		err = scanAll(db, `SELECT card_id, tag FROM card_tags ORDER BY tag`, nil, func(sc func(...any) error) error {
			var id, tag string
			if err := sc(&id, &tag); err != nil {
				return err
			}
			tags[id] = append(tags[id], tag)
			return nil
		})
		if err != nil {
			return err
		}
		for _, row := range cards {
			if t := tags[row["id"].(string)]; t != nil {
				row["tags"] = t
			}
			add("cards", row)
		}
	}

	// Comments: soft-deleted ones go up as deletes only.
	err = scanAll(db, `SELECT id, card_id, body, agent, created_at, updated_at, deleted_at FROM card_comments
		WHERE updated_at >= ? OR (deleted_at <> '' AND deleted_at >= ?)`, []any{since, since}, func(sc func(...any) error) error {
		var id, card, body, agent, created, updated, deleted string
		if err := sc(&id, &card, &body, &agent, &created, &updated, &deleted); err != nil {
			return err
		}
		if deleted != "" {
			del("card_comments", id)
			return nil
		}
		add("card_comments", map[string]any{"id": id, "cardId": card, "body": body, "agent": agent, "createdAt": created, "updatedAt": updated})
		return nil
	})
	if err != nil {
		return err
	}

	err = scanAll(db, `SELECT id, card_id, kind, target_card_id, url, title, agent, created_at, updated_at FROM card_links WHERE updated_at >= ?`,
		[]any{since}, func(sc func(...any) error) error {
			var id, card, kind, target, url, title, agent, created, updated string
			if err := sc(&id, &card, &kind, &target, &url, &title, &agent, &created, &updated); err != nil {
				return err
			}
			add("card_links", map[string]any{"id": id, "cardId": card, "kind": kind, "targetCardId": nullStr(target), "url": url, "title": title,
				"agent": agent, "createdAt": created, "updatedAt": updated})
			return nil
		})
	if err != nil {
		return err
	}

	err = scanAll(db, `SELECT id, card_id, title, position, created_at, updated_at FROM card_checklists WHERE updated_at >= ?`,
		[]any{since}, func(sc func(...any) error) error {
			var id, card, title, created, updated string
			var pos int
			if err := sc(&id, &card, &title, &pos, &created, &updated); err != nil {
				return err
			}
			add("card_checklists", map[string]any{"id": id, "cardId": card, "title": title, "position": pos, "createdAt": created, "updatedAt": updated})
			return nil
		})
	if err != nil {
		return err
	}

	err = scanAll(db, `SELECT i.id, i.checklist_id, l.card_id, i.title, i.done, i.position, i.done_at, i.done_by, i.created_at, i.updated_at
		FROM card_checklist_items i JOIN card_checklists l ON l.id = i.checklist_id WHERE i.updated_at >= ?`,
		[]any{since}, func(sc func(...any) error) error {
			var id, list, card, title, doneAt, doneBy, created, updated string
			var done, pos int
			if err := sc(&id, &list, &card, &title, &done, &pos, &doneAt, &doneBy, &created, &updated); err != nil {
				return err
			}
			add("card_checklist_items", map[string]any{"id": id, "checklistId": list, "cardId": card, "title": title, "done": done == 1,
				"position": pos, "doneAt": nullStr(doneAt), "doneBy": doneBy, "createdAt": created, "updatedAt": updated})
			return nil
		})
	if err != nil {
		return err
	}

	// Activity is append-only; lines from before GRS-47 get their UUID here.
	if err := store.EnsureEventUIDs(db); err != nil {
		return err
	}
	return scanAll(db, `SELECT uid, card_id, at, agent, kind, detail FROM card_events WHERE at >= ?`, []any{since}, func(sc func(...any) error) error {
		var id, card, at, agent, kind, detail string
		if err := sc(&id, &card, &at, &agent, &kind, &detail); err != nil {
			return err
		}
		add("card_events", map[string]any{"id": id, "cardId": card, "at": at, "agent": agent, "kind": kind, "detail": detail})
		return nil
	})
}

// applyCardResults takes what the API decided: merged state ids and card
// numbers and keys. It reports whether any card table answered at all.
func applyCardResults(db *sql.DB, cs *cardSync, results []api.PushResult, r *Report) (answered bool, err error) {
	// Keys as they were before this push: one card's new number can move
	// another out of the way before its own result is read.
	before := map[string]string{}
	err = scanAll(db, `SELECT id, key FROM cards`, nil, func(sc func(...any) error) error {
		var id, key string
		if err := sc(&id, &key); err != nil {
			return err
		}
		before[id] = key
		return nil
	})
	if err != nil {
		return false, err
	}
	for _, res := range results {
		if !isCardTable(res.Table) {
			continue
		}
		answered = true
		if res.Error != "" || res.Assigned == nil {
			continue
		}
		a := res.Assigned
		switch res.Table {
		case "card_states":
			if a.ID != "" && !strings.EqualFold(a.ID, res.ID) {
				if err := inTx(db, func(tx *sql.Tx) error { return remapState(tx, res.ID, strings.ToLower(a.ID)) }); err != nil {
					return answered, fmt.Errorf("state %s: %w", short(res.ID), err)
				}
			}
		case "cards":
			if a.Number <= 0 {
				continue
			}
			key := a.Key
			if key == "" {
				key = cs.prefix + "-" + strconv.Itoa(a.Number)
			}
			if cs.prefix == "" {
				cs.prefix = keyPrefix(key)
			}
			oldKey := before[res.ID]
			err := inTx(db, func(tx *sql.Tx) error {
				if err := setNumber(tx, res.ID, a.Number, key); err != nil {
					return err
				}
				if a.StateID != "" {
					var n int
					if err := tx.QueryRow(`SELECT COUNT(*) FROM card_states WHERE id = ?`, a.StateID).Scan(&n); err != nil {
						return err
					}
					if n > 0 {
						if _, err := tx.Exec(`UPDATE cards SET state_id = ? WHERE id = ?`, a.StateID, res.ID); err != nil {
							return err
						}
					}
				}
				return nil
			})
			if err != nil {
				return answered, fmt.Errorf("card %s: %w", short(res.ID), err)
			}
			if oldKey != "" && !strings.EqualFold(oldKey, key) {
				r.CardsRenumbered++
				r.Notes = append(r.Notes, fmt.Sprintf("card %s is %s on the API (its number was taken)", oldKey, key))
			}
		}
	}
	return answered, syncSequence(db)
}

// setNumber gives a card its API number and key. A local card already
// holding either moves to the next free number (its own push settles it).
func setNumber(tx *sql.Tx, id string, n int, key string) error {
	rows, err := tx.Query(`SELECT id, key FROM cards WHERE (number = ? OR upper(key) = upper(?)) AND id <> ?`, n, key, id)
	if err != nil {
		return err
	}
	type holder struct{ id, key string }
	var hs []holder
	for rows.Next() {
		var h holder
		if err := rows.Scan(&h.id, &h.key); err != nil {
			rows.Close()
			return err
		}
		hs = append(hs, h)
	}
	rows.Close()
	for _, h := range hs {
		var top, last int
		if err := tx.QueryRow(`SELECT COALESCE(MAX(number), 0) FROM cards`).Scan(&top); err != nil {
			return err
		}
		_ = tx.QueryRow(`SELECT last_number FROM card_sequence WHERE id = 1`).Scan(&last)
		next := max(top, last, n) + 1
		pre := keyPrefix(h.key)
		if pre == "" {
			pre = keyPrefix(key)
		}
		if _, err := tx.Exec(`UPDATE cards SET number = ?, key = ? WHERE id = ?`, next, pre+"-"+strconv.Itoa(next), h.id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE cards SET number = ?, key = ? WHERE id = ?`, n, key, id)
	return err
}

// syncSequence keeps card_sequence at or past every number in the cache, so
// a card made offline proposes the next number the API is likely to give.
func syncSequence(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO card_sequence (id, last_number) VALUES (1, (SELECT COALESCE(MAX(number), 0) FROM cards))
		ON CONFLICT(id) DO UPDATE SET last_number = MAX(last_number, excluded.last_number)`)
	return err
}

func keyPrefix(key string) string {
	if i := strings.LastIndexByte(key, '-'); i > 0 {
		return key[:i]
	}
	return ""
}

// ------------------------------------------------------------------ pull

// sortForApply orders one pull page so a row lands after what it points at:
// board upserts in push order, then board deletes children first. Other
// tables keep their place, ahead of the board.
func sortForApply(ch []api.Change) {
	rank := func(c api.Change) int {
		r := cardRank(c.Table)
		if r == 0 {
			return 0
		}
		if c.Deleted {
			return 100 - r
		}
		return r
	}
	sort.SliceStable(ch, func(i, j int) bool { return rank(ch[i]) < rank(ch[j]) })
}

type stateRow struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Position   int       `json:"position"`
	IsDefault  bool      `json:"isDefault"`
	IsTerminal bool      `json:"isTerminal"`
	WIPLimit   int       `json:"wipLimit"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type cardRow struct {
	ID              string          `json:"id"`
	Number          int             `json:"number"`
	Key             string          `json:"key"`
	Type            string          `json:"type"`
	Title           string          `json:"title"`
	Body            string          `json:"body"`
	CardData        json.RawMessage `json:"cardData"`
	StateID         string          `json:"stateId"`
	State           string          `json:"state"`
	Size            string          `json:"size"`
	Priority        int             `json:"priority"`
	Position        int             `json:"position"`
	AssigneeLabel   string          `json:"assigneeLabel"`
	ParentID        *string         `json:"parentId"`
	DueAt           *time.Time      `json:"dueAt"`
	EstimateMinutes int             `json:"estimateMinutes"`
	Artifact        string          `json:"artifact"`
	ExternalKey     string          `json:"externalKey"`
	Tags            []string        `json:"tags"`
	CreatedBy       string          `json:"createdBy"`
	UpdatedBy       string          `json:"updatedBy"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	CompletedAt     *time.Time      `json:"completedAt"`
}

type commentRow struct {
	ID        string    `json:"id"`
	CardID    string    `json:"cardId"`
	Body      string    `json:"body"`
	Agent     string    `json:"agent"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type linkRow struct {
	ID           string    `json:"id"`
	CardID       string    `json:"cardId"`
	Kind         string    `json:"kind"`
	TargetCardID *string   `json:"targetCardId"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Agent        string    `json:"agent"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type checklistRow struct {
	ID        string    `json:"id"`
	CardID    string    `json:"cardId"`
	Title     string    `json:"title"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type itemRow struct {
	ID          string     `json:"id"`
	ChecklistID string     `json:"checklistId"`
	Title       string     `json:"title"`
	Done        bool       `json:"done"`
	Position    int        `json:"position"`
	DoneAt      *time.Time `json:"doneAt"`
	DoneBy      string     `json:"doneBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type eventRow struct {
	ID     string    `json:"id"`
	CardID string    `json:"cardId"`
	At     time.Time `json:"at"`
	Agent  string    `json:"agent"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
}

// applyCard applies one pulled board change. A row whose local copy is
// newer is left for the next push (last write wins); a child whose card
// (or checklist) is not in the cache is skipped.
func applyCard(db *sql.DB, ch api.Change) (bool, error) {
	if ch.Deleted {
		return applyCardDelete(db, ch)
	}
	if tombstoned(db, ch.Table, ch.ID) {
		return false, nil
	}
	switch ch.Table {
	case "card_states":
		var s stateRow
		if err := json.Unmarshal(ch.Row, &s); err != nil {
			return false, err
		}
		return applyState(db, s)
	case "cards":
		var c cardRow
		if err := json.Unmarshal(ch.Row, &c); err != nil {
			return false, err
		}
		return applyCardRow(db, c)
	case "card_comments":
		var x commentRow
		if err := json.Unmarshal(ch.Row, &x); err != nil {
			return false, err
		}
		if !has(db, `SELECT COUNT(*) FROM cards WHERE id = ?`, x.CardID) || newerHere(db, "card_comments", x.ID, x.UpdatedAt) {
			return false, nil
		}
		_, err := db.Exec(`INSERT INTO card_comments (id, card_id, body, agent, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, '')
			ON CONFLICT(id) DO UPDATE SET body = excluded.body, agent = excluded.agent, updated_at = excluded.updated_at, deleted_at = ''`,
			x.ID, x.CardID, x.Body, x.Agent, stamp(x.CreatedAt), stamp(x.UpdatedAt))
		return err == nil, err
	case "card_links":
		var x linkRow
		if err := json.Unmarshal(ch.Row, &x); err != nil {
			return false, err
		}
		if !has(db, `SELECT COUNT(*) FROM cards WHERE id = ?`, x.CardID) || newerHere(db, "card_links", x.ID, x.UpdatedAt) {
			return false, nil
		}
		_, err := db.Exec(`INSERT INTO card_links (id, card_id, kind, target_card_id, url, title, agent, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, target_card_id = excluded.target_card_id, url = excluded.url, title = excluded.title,
				updated_at = excluded.updated_at`,
			x.ID, x.CardID, x.Kind, derefStr(x.TargetCardID), x.URL, x.Title, x.Agent, stamp(x.CreatedAt), stamp(x.UpdatedAt))
		return err == nil, err
	case "card_checklists":
		var x checklistRow
		if err := json.Unmarshal(ch.Row, &x); err != nil {
			return false, err
		}
		if !has(db, `SELECT COUNT(*) FROM cards WHERE id = ?`, x.CardID) || newerHere(db, "card_checklists", x.ID, x.UpdatedAt) {
			return false, nil
		}
		_, err := db.Exec(`INSERT INTO card_checklists (id, card_id, title, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET title = excluded.title, position = excluded.position, updated_at = excluded.updated_at`,
			x.ID, x.CardID, x.Title, x.Position, stamp(x.CreatedAt), stamp(x.UpdatedAt))
		return err == nil, err
	case "card_checklist_items":
		var x itemRow
		if err := json.Unmarshal(ch.Row, &x); err != nil {
			return false, err
		}
		if !has(db, `SELECT COUNT(*) FROM card_checklists WHERE id = ?`, x.ChecklistID) || newerHere(db, "card_checklist_items", x.ID, x.UpdatedAt) {
			return false, nil
		}
		doneAt := ""
		if x.Done && x.DoneAt != nil {
			doneAt = stamp(*x.DoneAt)
		}
		_, err := db.Exec(`INSERT INTO card_checklist_items (id, checklist_id, title, done, position, done_at, done_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET title = excluded.title, done = excluded.done, position = excluded.position, done_at = excluded.done_at,
				done_by = excluded.done_by, updated_at = excluded.updated_at`,
			x.ID, x.ChecklistID, x.Title, b2i(x.Done), x.Position, doneAt, x.DoneBy, stamp(x.CreatedAt), stamp(x.UpdatedAt))
		return err == nil, err
	case "card_events":
		var x eventRow
		if err := json.Unmarshal(ch.Row, &x); err != nil {
			return false, err
		}
		if !has(db, `SELECT COUNT(*) FROM cards WHERE id = ?`, x.CardID) || has(db, `SELECT COUNT(*) FROM card_events WHERE uid = ?`, x.ID) {
			return false, nil
		}
		_, err := db.Exec(`INSERT INTO card_events (card_id, at, agent, kind, detail, uid) VALUES (?, ?, ?, ?, ?, ?)`,
			x.CardID, stamp(x.At), x.Agent, x.Kind, x.Detail, x.ID)
		return err == nil, err
	}
	return false, nil
}

func applyState(db *sql.DB, s stateRow) (bool, error) {
	upd := stamp(s.UpdatedAt)
	applied := false
	err := inTx(db, func(tx *sql.Tx) error {
		var local string
		err := tx.QueryRow(`SELECT updated_at FROM card_states WHERE id = ?`, s.ID).Scan(&local)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && upd < local {
			return nil
		}
		// A local state holding this name under another id is the same
		// swimlane: it becomes this one.
		var other string
		err = tx.QueryRow(`SELECT id FROM card_states WHERE lower(name) = lower(?) AND id <> ?`, s.Name, s.ID).Scan(&other)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if other != "" {
			if err := remapState(tx, other, s.ID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO card_states (id, name, position, is_default, is_terminal, wip_limit, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name, position = excluded.position, is_default = excluded.is_default,
				is_terminal = excluded.is_terminal, wip_limit = excluded.wip_limit, updated_at = excluded.updated_at`,
			s.ID, s.Name, s.Position, b2i(s.IsDefault), b2i(s.IsTerminal), s.WIPLimit, stamp(s.CreatedAt), upd); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

func applyCardRow(db *sql.DB, c cardRow) (bool, error) {
	upd := stamp(c.UpdatedAt)
	key := c.Key
	if key == "" {
		var pre string
		_ = db.QueryRow(`SELECT key FROM cards WHERE id = ?`, c.ID).Scan(&pre)
		key = keyPrefix(pre) + "-" + strconv.Itoa(c.Number)
	}
	applied := false
	err := inTx(db, func(tx *sql.Tx) error {
		var local string
		err := tx.QueryRow(`SELECT updated_at FROM cards WHERE id = ?`, c.ID).Scan(&local)
		exists := err == nil
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		// The number and key are the API's whatever the fields do.
		if err := setNumber(tx, c.ID, c.Number, key); err != nil {
			return err
		}
		if exists && upd < local {
			return nil // a newer local edit goes up next push
		}
		stateID, err := localStateFor(tx, c.StateID, c.State)
		if err != nil {
			return err
		}
		data := strings.TrimSpace(string(c.CardData))
		if data == "" || data == "null" {
			data = "{}"
		}
		due, completed := "", ""
		if c.DueAt != nil {
			due = stamp(*c.DueAt)
		}
		if c.CompletedAt != nil {
			completed = stamp(*c.CompletedAt)
		}
		updatedBy := c.UpdatedBy
		if updatedBy == "" {
			updatedBy = c.CreatedBy
		}
		if _, err := tx.Exec(`INSERT INTO cards (id, number, key, title, body, type, state_id, size, priority, position, card_data, assignee, parent_id, due_at,
				estimate_minutes, artifact, external_key, created_by, updated_by, created_at, updated_at, completed_at, deleted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')
			ON CONFLICT(id) DO UPDATE SET number = excluded.number, key = excluded.key, title = excluded.title, body = excluded.body, type = excluded.type,
				state_id = excluded.state_id, size = excluded.size, priority = excluded.priority, position = excluded.position, card_data = excluded.card_data,
				assignee = excluded.assignee, parent_id = excluded.parent_id, due_at = excluded.due_at, estimate_minutes = excluded.estimate_minutes,
				artifact = excluded.artifact, external_key = excluded.external_key, updated_by = excluded.updated_by, updated_at = excluded.updated_at,
				completed_at = excluded.completed_at, deleted_at = ''`,
			c.ID, c.Number, key, c.Title, c.Body, c.Type, stateID, c.Size, c.Priority, c.Position, data, c.AssigneeLabel, derefStr(c.ParentID), due,
			c.EstimateMinutes, c.Artifact, c.ExternalKey, c.CreatedBy, updatedBy, stamp(c.CreatedAt), upd, completed); err != nil {
			return err
		}
		if c.Tags != nil {
			if _, err := tx.Exec(`DELETE FROM card_tags WHERE card_id = ?`, c.ID); err != nil {
				return err
			}
			for _, t := range c.Tags {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO card_tags (card_id, tag) VALUES (?, ?)`, c.ID, t); err != nil {
					return err
				}
			}
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, syncSequence(db)
}

// localStateFor is the state a pulled card goes in: its id, else its name,
// else the default, else the first.
func localStateFor(tx *sql.Tx, id, name string) (string, error) {
	var out string
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`SELECT id FROM card_states WHERE id = ?`, []any{strings.ToLower(id)}},
		{`SELECT id FROM card_states WHERE lower(name) = lower(?)`, []any{name}},
		{`SELECT id FROM card_states WHERE is_default = 1 ORDER BY position LIMIT 1`, nil},
		{`SELECT id FROM card_states ORDER BY position LIMIT 1`, nil},
	} {
		err := tx.QueryRow(q.sql, q.args...).Scan(&out)
		if err == nil {
			return out, nil
		}
		if err != sql.ErrNoRows {
			return "", err
		}
	}
	return "", fmt.Errorf("no card states in the cache")
}

// applyCardDelete applies a pulled delete. Cards and comments are
// soft-deleted (the number is never reused); links, checklists and items go;
// a state goes when no live card is in it; activity is never deleted.
func applyCardDelete(db *sql.DB, ch api.Change) (bool, error) {
	now := store.Now()
	switch ch.Table {
	case "cards":
		applied := false
		err := inTx(db, func(tx *sql.Tx) error {
			res, err := tx.Exec(`UPDATE cards SET deleted_at = ? WHERE id = ? AND deleted_at = ''`, now, ch.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				applied = true
				_, err = tx.Exec(`UPDATE cards SET parent_id = '' WHERE parent_id = ?`, ch.ID)
			}
			return err
		})
		return applied, err
	case "card_comments":
		res, err := db.Exec(`UPDATE card_comments SET deleted_at = ? WHERE id = ? AND deleted_at = ''`, now, ch.ID)
		return affected(res), err
	case "card_links", "card_checklist_items":
		res, err := db.Exec(`DELETE FROM `+ch.Table+` WHERE id = ?`, ch.ID)
		return affected(res), err
	case "card_checklists":
		applied := false
		err := inTx(db, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`DELETE FROM card_checklist_items WHERE checklist_id = ?`, ch.ID); err != nil {
				return err
			}
			res, err := tx.Exec(`DELETE FROM card_checklists WHERE id = ?`, ch.ID)
			applied = affected(res)
			return err
		})
		return applied, err
	case "card_states":
		applied := false
		err := inTx(db, func(tx *sql.Tx) error {
			var live int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM cards WHERE state_id = ? AND deleted_at = ''`, ch.ID).Scan(&live); err != nil {
				return err
			}
			if live > 0 {
				return nil
			}
			var def string
			_ = tx.QueryRow(`SELECT id FROM card_states WHERE id <> ? ORDER BY is_default DESC, position LIMIT 1`, ch.ID).Scan(&def)
			if def == "" {
				return nil // never leave the board without a state
			}
			if _, err := tx.Exec(`UPDATE cards SET state_id = ? WHERE state_id = ?`, def, ch.ID); err != nil {
				return err
			}
			res, err := tx.Exec(`DELETE FROM card_states WHERE id = ?`, ch.ID)
			applied = affected(res)
			return err
		})
		return applied, err
	}
	return false, nil
}

// ---------------------------------------------------------------- helpers

func scanAll(db *sql.DB, q string, args []any, fn func(scan func(...any) error) error) error {
	rows, err := db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

func has(db *sql.DB, q string, args ...any) bool {
	var n int
	_ = db.QueryRow(q, args...).Scan(&n)
	return n > 0
}

// newerHere reports a local row edited after the pulled one.
func newerHere(db *sql.DB, tbl, id string, pulled time.Time) bool {
	var local string
	if err := db.QueryRow(`SELECT updated_at FROM `+tbl+` WHERE id = ?`, id).Scan(&local); err != nil {
		return false
	}
	return local > stamp(pulled)
}

func tombstoned(db *sql.DB, tbl, id string) bool {
	return has(db, `SELECT COUNT(*) FROM sync_deletes WHERE tbl = ? AND id = ?`, tbl, id)
}

func affected(res sql.Result) bool {
	if res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// stamp is the cache's time format (RFC 3339, UTC, to the second); the
// zero time is "", which the API reads as none.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// nullStr turns the cache's "" (none) into JSON null.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
