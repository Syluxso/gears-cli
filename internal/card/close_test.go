package card

import (
	"strings"
	"testing"
	"time"

	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/store"
)

func testProject(t *testing.T) *project.Project {
	t.Helper()
	res, err := project.Init(t.TempDir(), "ct", "CT")
	if err != nil {
		t.Fatal(err)
	}
	return res.Project
}

func newTestCard(t *testing.T, p *project.Project, title string) *Card {
	t.Helper()
	c, err := New(p, Input{Type: "task", Title: title, CardData: map[string]any{
		"task": map[string]any{"what": title, "soThat": "it is tested"}, "acceptance": []any{"it closes, proven by the test"}}}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func keys(cs []Card) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return strings.Join(out, ",")
}

// rawCard reads the stored columns a card's struct parses.
func rawCard(t *testing.T, p *project.Project, id string) (updated, updatedBy, closed, stateChanged string) {
	t.Helper()
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT updated_at, updated_by, closed_at, state_changed_at FROM cards WHERE id = ?`, id).Scan(&updated, &updatedBy, &closed, &stateChanged); err != nil {
		t.Fatal(err)
	}
	return
}

func lastEvent(t *testing.T, p *project.Project, key string) Event {
	t.Helper()
	f, err := Show(p, key)
	if err != nil {
		t.Fatal(err)
	}
	return f.Events[len(f.Events)-1]
}

// GRS-50: close takes a card off the board (list hides it unless --closed
// or --all), keeps its state, bumps updated_at/by and logs a line; reopen
// puts it back. Each refuses the wrong way round.
func TestCloseReopenAndListFilters(t *testing.T) {
	p := testProject(t)
	a := newTestCard(t, p, "Alpha")
	b := newTestCard(t, p, "Beta")
	if _, _, closed, sc := rawCard(t, p, a.ID); closed != "" || sc == "" {
		t.Fatalf("new card: closed %q stateChanged %q", closed, sc)
	}

	c, err := Close(p, a.Key, "won't do", "closer")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Closed() || c.State != "Backlog" {
		t.Fatalf("closed card: %+v", c)
	}
	_, by, closed, _ := rawCard(t, p, a.ID)
	if by != "closer" || closed == "" {
		t.Fatalf("after close: by %q closed %q", by, closed)
	}
	if e := lastEvent(t, p, a.Key); e.Kind != "closed" || e.Detail != "won't do" || e.Agent != "closer" {
		t.Fatalf("close event: %+v", e)
	}
	if _, err := Close(p, a.Key, "", "closer"); err == nil || !strings.Contains(err.Error(), "closed already") {
		t.Fatalf("second close: %v", err)
	}

	for _, tc := range []struct {
		name string
		f    Filter
		want string
	}{
		{"default hides closed", Filter{}, b.Key},
		{"state hides closed", Filter{State: "Backlog"}, b.Key},
		{"closed only", Filter{Closed: true}, a.Key},
		{"all shows both", Filter{All: true}, a.Key + "," + b.Key},
	} {
		got, err := List(p, tc.f)
		if err != nil {
			t.Fatal(err)
		}
		if keys(got) != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, keys(got), tc.want)
		}
	}
	counts, err := ClosedCounts(p)
	if err != nil || counts["Backlog"] != 1 || len(counts) != 1 {
		t.Fatalf("closed counts: %v %v", counts, err)
	}

	r, err := Reopen(p, a.Key, "", "opener")
	if err != nil {
		t.Fatal(err)
	}
	if r.Closed() || r.State != "Backlog" {
		t.Fatalf("reopened card: %+v", r)
	}
	if e := lastEvent(t, p, a.Key); e.Kind != "reopened" || e.Agent != "opener" {
		t.Fatalf("reopen event: %+v", e)
	}
	if _, err := Reopen(p, a.Key, "", "opener"); err == nil || !strings.Contains(err.Error(), "not closed") {
		t.Fatalf("second reopen: %v", err)
	}
	if got, _ := List(p, Filter{}); keys(got) != a.Key+","+b.Key {
		t.Fatalf("after reopen: %s", keys(got))
	}
}

// A closed card moved into a non-terminal state is reopened; moved within
// the terminal state it stays closed. Every state change stamps
// state_changed_at, a reorder does not.
func TestMoveReopensAndStampsStateChange(t *testing.T) {
	p := testProject(t)
	a := newTestCard(t, p, "Alpha")
	newTestCard(t, p, "Beta")
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	old := "2026-01-01T00:00:00Z"
	if _, err := db.Exec(`UPDATE cards SET state_changed_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, _, err := Move(p, a.Key, "", "top", "tester"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, sc := rawCard(t, p, a.ID); sc != old {
		t.Fatalf("reorder stamped state_changed_at: %s", sc)
	}
	if _, _, err := Move(p, a.Key, "Completed", "", "tester"); err != nil {
		t.Fatal(err)
	}
	_, _, _, sc := rawCard(t, p, a.ID)
	if sc == old || time.Since(store.Parse(sc)) > time.Minute {
		t.Fatalf("move did not stamp state_changed_at: %s", sc)
	}

	if _, err := Close(p, a.Key, "", "tester"); err != nil {
		t.Fatal(err)
	}
	// Within the terminal state: still closed.
	if c, _, err := Move(p, a.Key, "Completed", "top", "tester"); err != nil || !c.Closed() {
		t.Fatalf("move within terminal: %+v %v", c, err)
	}
	c, _, err := Move(p, a.Key, "In Progress", "", "mover")
	if err != nil {
		t.Fatal(err)
	}
	if c.Closed() || c.State != "In Progress" || !c.CompletedAt.IsZero() {
		t.Fatalf("move out of terminal: %+v", c)
	}
	f, err := Show(p, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range f.Events {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, " "); !strings.HasSuffix(got, "closed moved moved reopened") {
		t.Fatalf("events: %s", got)
	}
}

// Claim moves a card out of the default state, stamping state_changed_at;
// claiming a closed card takes it back onto the board.
func TestClaimStampsAndReopens(t *testing.T) {
	p := testProject(t)
	a := newTestCard(t, p, "Alpha")
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	old := "2026-01-01T00:00:00Z"
	if _, err := db.Exec(`UPDATE cards SET state_changed_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Close(p, a.Key, "", "tester"); err != nil {
		t.Fatal(err)
	}
	c, _, err := Claim(p, a.Key, "claimer")
	if err != nil {
		t.Fatal(err)
	}
	if c.Closed() || c.State != "In Progress" || c.Assignee != "claimer" {
		t.Fatalf("claimed: %+v", c)
	}
	if _, _, _, sc := rawCard(t, p, a.ID); sc == old {
		t.Fatal("claim did not stamp state_changed_at")
	}

	// Past the default state already: claim reopens in place.
	if _, err := Close(p, a.Key, "", "tester"); err != nil {
		t.Fatal(err)
	}
	if c, _, err = Claim(p, a.Key, "claimer"); err != nil || c.Closed() || c.State != "In Progress" {
		t.Fatalf("claim of closed card in progress: %+v %v", c, err)
	}
}
