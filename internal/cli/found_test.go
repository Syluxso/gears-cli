package cli

import (
	"reflect"
	"testing"
)

// GRS-99: gears runbook next prints what earlier card.query steps found.
func TestFoundLines(t *testing.T) {
	got := foundLines(map[string]any{
		"scope": map[string]any{"count": 2.0, "cards": []any{map[string]any{"key": "GRS-1"}, map[string]any{"key": "GRS-2"}}},
		"again": map[string]any{"count": 0.0, "cards": []any{}},
		"other": map[string]any{"note": "not a query"},
	})
	want := []string{"again: 0 cards", "scope: 2 cards: GRS-1, GRS-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if foundLines(nil) != nil {
		t.Fatal("nil outputs")
	}
}
