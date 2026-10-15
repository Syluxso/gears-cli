// Package people is who a desk item can be for (GRS-36): the workspace's
// people as gears-api lists them, each with the agent labels their devices
// use. Read from the API when logged in and cached in the workspace's
// local record; offline, the cached list answers, marked stale. The cache
// also remembers which person this install is (me), for the local desk's
// "mine" view.
package people

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

const cacheKey = "people"

// Timeout bounds the API read: an unreachable API falls back to the cache.
var Timeout = 3 * time.Second

// List is the workspace's people and where they came from.
type List struct {
	Me        string       `json:"me,omitempty"`
	People    []api.Person `json:"people"`
	FetchedAt string       `json:"fetchedAt,omitempty"`
	// Stale is true when the API did not answer and this is the cached copy
	// (empty when nothing was ever cached).
	Stale bool   `json:"stale"`
	Note  string `json:"note,omitempty"`
}

type cached struct {
	Me     string       `json:"me"`
	People []api.Person `json:"people"`
	At     string       `json:"at"`
}

// Get reads the people from the API through cl (nil: offline), caching
// them; on any failure it answers the cache, stale.
func Get(ctx context.Context, p *project.Project, cl *api.Client, offlineWhy string) (*List, error) {
	if cl != nil {
		cctx, cancel := context.WithTimeout(ctx, Timeout)
		out, err := cl.People(cctx, p.WorkspaceID())
		cancel()
		if err == nil {
			if out.People == nil {
				out.People = []api.Person{}
			}
			c := cached{Me: out.Me, People: out.People, At: store.Now()}
			if err := save(p, c); err != nil {
				return nil, err
			}
			return &List{Me: c.Me, People: c.People, FetchedAt: c.At}, nil
		}
		offlineWhy = err.Error()
	}
	c, err := load(p)
	if err != nil {
		return nil, err
	}
	l := &List{Me: c.Me, People: c.People, FetchedAt: c.At, Stale: true, Note: offlineWhy}
	if l.People == nil {
		l.People = []api.Person{}
	}
	if c.At == "" {
		l.Note = "no cached people yet (" + offlineWhy + "); run gears login, then people_list again"
	}
	return l, nil
}

// Me is the person this install is, from the last people read ("" when
// never read).
func Me(p *project.Project) string {
	if !store.Exists(p) {
		return ""
	}
	c, err := load(p)
	if err != nil {
		return ""
	}
	return c.Me
}

// Cached is the last people list read, without asking the API.
func Cached(p *project.Project) []api.Person {
	if !store.Exists(p) {
		return nil
	}
	c, _ := load(p)
	return c.People
}

func save(p *project.Project, c cached) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	db, err := store.Open(p)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO sync_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, cacheKey, string(b))
	return err
}

func load(p *project.Project) (cached, error) {
	var c cached
	db, err := store.Open(p)
	if err != nil {
		return c, err
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, cacheKey).Scan(&raw); err != nil {
		return c, nil // no cache yet is not an error
	}
	_ = json.Unmarshal([]byte(raw), &c)
	return c, nil
}
