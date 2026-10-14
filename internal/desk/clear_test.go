package desk

import (
	"testing"

	"github.com/Syluxso/gears-cli/internal/project"
)

func TestClear(t *testing.T) {
	res, err := project.Init(t.TempDir(), "dt", "DT")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	send := func(typ, to string) {
		t.Helper()
		if _, err := Send(p, typ, "sender", to, typ+" to "+to, ""); err != nil {
			t.Fatal(err)
		}
	}
	send("handoff", "me")
	send("work", "me")
	send("ask", "me")
	send("handoff", "")
	send("info", "other")

	mine, err := Clear(p, "me", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 3 {
		t.Fatalf("mine cleared %d, want 3 (handoff, work, ask to me)", len(mine))
	}
	left, _ := List(p, Filter{})
	if len(left) != 2 {
		t.Fatalf("after mine, %d open, want 2", len(left))
	}

	all, err := Clear(p, "me", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all cleared %d, want 2", len(all))
	}
	if left, _ := List(p, Filter{}); len(left) != 0 {
		t.Fatalf("after all, %d open, want 0", len(left))
	}
	for _, it := range append(mine, all...) {
		if it.State != "closed" || it.ClosedBy != "me" {
			t.Fatalf("%s: state %s closed by %q", it.Short(), it.State, it.ClosedBy)
		}
	}
}
