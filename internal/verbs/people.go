package verbs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/desk"
	"github.com/Syluxso/gears-cli/internal/people"
)

// People (GRS-36): who a desk item can be for.

type PeopleListIn struct{}

// PeopleList is the workspace's people from gears-api (cached locally);
// offline it answers the cached list with stale: true.
func PeopleList(c Ctx, _ PeopleListIn) (*people.List, error) {
	return peopleGet(c)
}

// peopleGet reads through the API when logged in. A var so tests can stand
// in for it.
var peopleGet = func(c Ctx) (*people.List, error) {
	cl, err := api.Open(api.Host())
	why := ""
	if err != nil {
		why = err.Error()
		if errors.Is(err, api.ErrNotLoggedIn) {
			why = "not logged in"
		}
		cl = nil
	}
	return people.Get(context.Background(), c.Project, cl, why)
}

// mine is the local desk's view for label (GRS-36): addressed to it or to
// any label the person behind this install uses, assigned to or authored
// by that person, or addressed to nobody. The person comes from the last
// people_list read; before one, only labels count.
func mine(c Ctx, label string) desk.Filter {
	f := desk.Filter{Agent: label}
	me := people.Me(c.Project)
	if me == "" {
		return f
	}
	f.User = me
	for _, p := range people.Cached(c.Project) {
		if p.ID == me {
			f.Labels = p.Labels
		}
	}
	return f
}

// checkAssignee refuses an assignedTo that the cached people list does not
// know, when there is one; the API checks again on its own create paths.
func checkAssignee(c Ctx, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	known := people.Cached(c.Project)
	if len(known) == 0 {
		return nil
	}
	for _, p := range known {
		if p.ID == id {
			return nil
		}
	}
	return fmt.Errorf("assignedTo %q is not a person of this workspace; call people_list and pass a person's id", id)
}
