package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-104: ticket-dev is an artifact prefix: artifact_new makes one from
// its shape, a hand-written ticket-dev--<card>--<label>.md is picked up,
// and the shape wants the card and the What Was Done section.
func TestTicketDevArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	t.Setenv("GEARS_AGENT", "tester")
	ws := t.TempDir()
	if err := runIn(t, ws, "init", "--slug", "ws", "--prefix", "WS"); err != nil {
		t.Fatal(err)
	}
	p, err := project.Find(ws)
	if err != nil {
		t.Fatal(err)
	}
	if pre, slug, ok := artifact.Parse("ticket-dev--grs-110--tab-icons.md"); !ok || pre != "ticket-dev" || slug != "grs-110--tab-icons" {
		t.Fatalf("parse: %q %q %v", pre, slug, ok)
	}
	a, err := verbs.ArtifactNew(verbs.Ctx{Project: p, Agent: "tester"}, verbs.ArtifactNewIn{Prefix: "ticket-dev", Title: "GRS-110 tab icons"})
	if err != nil {
		t.Fatal(err)
	}
	if a.File != "ticket-dev--grs-110-tab-icons.md" {
		t.Fatalf("file: %s", a.File)
	}
	path := filepath.Join(ws, project.Dir, "artifacts", a.File)
	seed, _ := os.ReadFile(path)
	if !strings.Contains(string(seed), "## What Was Done") {
		t.Fatalf("seed:\n%s", seed)
	}
	body := "---\ntitle: GRS-110 tab icons\ncard: GRS-110\n---\n\n# GRS-110 tab icons\n\n## What Was Done\n\n- Tab icons styled as currentColor outlines; icons only on phones.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	probs, err := verbs.ArtifactValidate(verbs.Ctx{Project: p, Agent: "tester"}, verbs.ArtifactValidateIn{File: a.File})
	if err != nil {
		t.Fatal(err)
	}
	if probs.Checked != 1 || len(probs.Problems) != 0 {
		t.Fatalf("filled ticket-dev artifact: %+v", probs)
	}
}
