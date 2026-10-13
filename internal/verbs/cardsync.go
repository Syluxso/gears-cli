package verbs

import (
	"fmt"
	"time"

	"github.com/Syluxso/gears-cli/internal/card"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/sync"
)

// Cards sync immediately (GRS-51): every card write pushes at once, every
// card read pulls first. Neither ever fails the verb: offline, the write
// stands locally and stays queued for the next push (another write, or
// gears sync), and the read answers from the cache.

// CardSync is a write's write-through result. Pushed false with a note:
// saved locally and queued.
type CardSync struct {
	Pushed bool   `json:"pushed"`
	Note   string `json:"note,omitempty"`
}

// Timeouts: a write waits at most this long for the API; a read's pull
// less, since the cache answers anyway.
var (
	writeThroughTimeout = 3 * time.Second
	backstopTimeout     = 1500 * time.Millisecond
)

// cardsUp and cardsDownFn are the network halves, swappable in tests.
var (
	cardsUp     = func(p *project.Project) (bool, string) { return sync.CardsUp(p, writeThroughTimeout) }
	cardsDownFn = func(p *project.Project) bool { return sync.CardsBackstop(p, backstopTimeout) }
)

// pushCards runs the write-through after a local card write. The note
// also goes to c.Notes (the CLI's stderr); the MCP result carries it.
func (c Ctx) pushCards() *CardSync {
	pushed, note := cardsUp(c.Project)
	if !pushed && note == "" {
		return nil // card sync off
	}
	if note != "" && c.Notes != nil {
		fmt.Fprintln(c.Notes, "gears: "+note)
	}
	return &CardSync{Pushed: pushed, Note: note}
}

// pushed is pushCards for a verb that answers with one card: the card is
// read back after a push, so it carries the number and key the API gave it.
func (c Ctx) pushed(cd *card.Card) Card {
	sy := c.pushCards()
	if sy != nil && sy.Pushed {
		if fresh, err := card.Get(c.Project, cd.ID); err == nil {
			cd = fresh
		}
	}
	o := cardOut(*cd)
	o.Sync = sy
	return o
}

// cardsDown is the backstop pull a card read makes first; true when the
// API did not answer in time and the read is from the cache as it was.
func (c Ctx) cardsDown() bool { return cardsDownFn(c.Project) }
