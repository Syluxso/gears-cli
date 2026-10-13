package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-50: the board hides closed cards, and every terminal lane says how
// many closed cards it holds; --all lists them, marked with their age.
func TestPrintBoardClosed(t *testing.T) {
	closedAt := time.Now().Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	states := []verbs.CardState{{Name: "Backlog", Count: 1, Default: true}, {Name: "In Progress"}, {Name: "Completed", Terminal: true, Closed: 2}}

	var b bytes.Buffer
	printBoard(&b, &verbs.CardListOut{States: states, Cards: []verbs.Card{{Key: "WS-1", Title: "Open", Type: "task", State: "Backlog", Priority: "P0"}}}, boardView{})
	got := b.String()
	for _, want := range []string{"Backlog (1)", "WS-1", "Completed  (--all lists them)\n  closed: 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("default board missing %q:\n%s", want, got)
		}
	}

	b.Reset()
	all := []verbs.Card{
		{Key: "WS-1", Title: "Open", Type: "task", State: "Backlog", Priority: "P0"},
		{Key: "WS-2", Title: "Done", Type: "task", State: "Completed", Priority: "P0"},
		{Key: "WS-3", Title: "Shut", Type: "task", State: "Completed", Priority: "P0", ClosedAt: closedAt},
	}
	states[2].Count = 2
	printBoard(&b, &verbs.CardListOut{States: states, Cards: all}, boardView{All: true})
	got = b.String()
	for _, want := range []string{"Completed (2)", "WS-2", "WS-3     Shut  [task P0 closed 3d ago]", "  closed: 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("--all board missing %q:\n%s", want, got)
		}
	}

	b.Reset()
	states = []verbs.CardState{{Name: "Backlog", Default: true}, {Name: "Completed", Terminal: true, Count: 1, Closed: 1}}
	printBoard(&b, &verbs.CardListOut{States: states, Cards: all[2:]}, boardView{Closed: true})
	got = b.String()
	if strings.Contains(got, "Backlog") || !strings.Contains(got, "WS-3") || strings.Contains(got, "closed: ") {
		t.Errorf("--closed board:\n%s", got)
	}
}
