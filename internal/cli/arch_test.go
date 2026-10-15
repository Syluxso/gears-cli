package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Syluxso/gears-cli/internal/artifact"
	"github.com/Syluxso/gears-cli/internal/project"
	"github.com/Syluxso/gears-cli/internal/verbs"
)

// GRS-41: init at a workspace root writes the stub arch--primary.md once;
// a re-run never overwrites it; a second arch artifact is refused; status
// carries the whole body and passes it free of any shape check.
func TestArchInitStatusAndOnePerWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GEARS_API", "http://127.0.0.1:1")
	t.Setenv("GEARS_AGENT", "tester")

	ws := t.TempDir()
	if err := runIn(t, ws, "init", "--slug", "ws", "--prefix", "WS"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws, project.Dir, "artifacts", artifact.ArchFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("init did not write the stub: %v", err)
	}
	for _, want := range []string{"## Context", "## Boundaries", "## Current Shape", "## Open Questions", "gears status always shows it"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("stub lacks %q:\n%s", want, data)
		}
	}

	// Edited, then init again: kept as written.
	mine := "# Architecture\n\nOne line, no sections at all.\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runIn(t, ws, "init"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != mine {
		t.Fatalf("re-run init overwrote the arch doc: %q", got)
	}

	c, err := ctxAt(ws)
	if err != nil {
		t.Fatal(err)
	}
	// A second one is refused, whatever its title.
	if _, err := verbs.ArtifactNew(c, verbs.ArtifactNewIn{Prefix: "arch", Title: "Another plan"}); err == nil || !strings.Contains(err.Error(), "one per workspace") {
		t.Fatalf("second arch artifact: want refusal, got %v", err)
	}

	out, err := verbs.Status(c, verbs.StatusIn{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Arch.File != artifact.ArchFile || out.Arch.Body != mine || out.Arch.Missing {
		t.Fatalf("status arch = %+v", out.Arch)
	}
	if out.Artifacts.ByPrefix["arch"] != 1 {
		t.Fatalf("arch count %v", out.Artifacts.ByPrefix)
	}
	// Free-form: no headings, short body, still no problem.
	for _, pr := range out.Artifacts.Problems {
		if strings.HasPrefix(pr, "arch--") {
			t.Fatalf("arch flagged by a shape check: %s", pr)
		}
	}
	var b bytes.Buffer
	printStatusBrief(&b, out, 100, false)
	if !strings.Contains(b.String(), "arch      "+artifact.ArchFile+"\n"+mine) {
		t.Fatalf("glance does not end with the arch doc:\n%s", b.String())
	}
	a, err := verbs.Arch(c, verbs.ArchIn{})
	if err != nil || a.Body != mine {
		t.Fatalf("gears arch: %+v %v", a, err)
	}
}

// An existing workspace without one: status says how to make it and does
// not write it; gears arch init does, and only once.
func TestArchMissingThenInit(t *testing.T) {
	ws := t.TempDir()
	res, err := project.Init(ws, "old", "OLD")
	if err != nil {
		t.Fatal(err)
	}
	c := verbs.Ctx{Project: res.Project, Agent: "tester"}
	out, err := verbs.Status(c, verbs.StatusIn{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Arch.Missing || !strings.Contains(out.Arch.Hint, "gears arch init") || out.Arch.Body != "" {
		t.Fatalf("missing arch: %+v", out.Arch)
	}
	if _, err := os.Stat(filepath.Join(res.Project.ArtifactsDir(), artifact.ArchFile)); !os.IsNotExist(err) {
		t.Fatal("status created the arch doc")
	}
	var b bytes.Buffer
	printStatusBrief(&b, out, 80, false)
	if !strings.Contains(b.String(), "arch      none: gears arch init") {
		t.Fatalf("glance does not say how to create it:\n%s", b.String())
	}

	in, err := verbs.ArchInit(c, verbs.ArchInitIn{})
	if err != nil || !in.Created || in.File != artifact.ArchFile {
		t.Fatalf("arch init: %+v %v", in, err)
	}
	again, err := verbs.ArchInit(c, verbs.ArchInitIn{})
	if err != nil || again.Created {
		t.Fatalf("second arch init: %+v %v", again, err)
	}
	// artifact new arch is the same door: refused now there is one.
	if _, err := verbs.ArtifactNew(c, verbs.ArtifactNewIn{Prefix: "arch", Title: "x"}); err == nil {
		t.Fatal("artifact new arch with one present: want refusal")
	}
}

// The glance prints up to 60 lines of the doc and points at gears arch for
// the rest; --full prints every line.
func TestArchGlanceLimit(t *testing.T) {
	var body strings.Builder
	for i := 1; i <= 75; i++ {
		fmt.Fprintf(&body, "line %d\n", i)
	}
	out := bigStatus()
	out.Arch = verbs.ArchOut{File: artifact.ArchFile, Body: body.String()}
	var b bytes.Buffer
	printStatusBrief(&b, out, 80, false)
	got := b.String()
	if !strings.Contains(got, "line 60\n") || strings.Contains(got, "line 61\n") || !strings.Contains(got, "… gears arch for the rest (15 more lines)") {
		t.Fatalf("glance limit wrong:\n%s", got[strings.Index(got, "arch "):])
	}

	short := bigStatus()
	short.Arch = verbs.ArchOut{File: artifact.ArchFile, Body: "a\nb\n"}
	b.Reset()
	printStatusBrief(&b, short, 80, false)
	if !strings.HasSuffix(b.String(), "arch      "+artifact.ArchFile+"\na\nb\n") || strings.Contains(b.String(), "for the rest") {
		t.Fatalf("short doc not shown whole:\n%s", b.String())
	}

	b.Reset()
	printArch(&b, out.Arch, 0, style{})
	if !strings.Contains(b.String(), "line 75\n") || strings.Contains(b.String(), "for the rest") {
		t.Fatal("full print cut the doc")
	}
}

func TestEditorCommand(t *testing.T) {
	t.Setenv("VISUAL", "code --wait")
	t.Setenv("EDITOR", "nano")
	if n, a := editorCommand(); n != "code" || len(a) != 1 || a[0] != "--wait" {
		t.Fatalf("VISUAL: %s %v", n, a)
	}
	t.Setenv("VISUAL", "")
	if n, _ := editorCommand(); n != "nano" {
		t.Fatalf("EDITOR: %s", n)
	}
	t.Setenv("EDITOR", "")
	if n, _ := editorCommand(); n != "notepad" && n != "vi" {
		t.Fatalf("default: %s", n)
	}
}
