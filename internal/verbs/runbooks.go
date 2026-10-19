package verbs

import (
	"errors"
	"strings"

	"github.com/Syluxso/gears-cli/internal/api"
	"github.com/Syluxso/gears-cli/internal/runs"
)

// Official and adopted runbooks (GRS-59), through gears-api. Official
// runbooks are frozen or editable; adopting copies one into the workspace.
// A frozen copy is read-only everywhere (gears-api refuses edits, and so do
// the local authoring tools) and its runs use the official steps; an
// editable copy is the workspace's own and runbook_edit changes it.

type RunbookAdoptIn struct {
	Ref string `json:"ref" jsonschema:"The official runbook to adopt (runbook_list official[].ref), e.g. ticket-development."`
	As  string `json:"as,omitempty" jsonschema:"Name for the copy (its slug is derived). Default: the ref for a frozen runbook, <ref>-copy for an editable one."`
}

// RunbookAdopt copies an official runbook into the workspace on gears-api.
func RunbookAdopt(c Ctx, in RunbookAdoptIn) (*api.AdoptedRunbook, error) {
	if strings.TrimSpace(in.Ref) == "" {
		return nil, errors.New("ref required: an official runbook (gears runbook list)")
	}
	ws, err := c.runsWorkspace()
	if err != nil {
		return nil, err
	}
	cl, err := runsClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := runCtx()
	defer cancel()
	out, err := cl.AdoptRunbook(ctx, ws, strings.TrimSpace(in.Ref), strings.TrimSpace(in.As))
	if err != nil {
		return nil, err
	}
	_ = runs.SaveRunbook(c.Project, *out)
	return out, nil
}

type RunbookEditIn struct {
	Runbook     string        `json:"runbook" jsonschema:"The adopted runbook's slug."`
	Title       *string       `json:"title,omitempty" jsonschema:"New title. Omit to keep."`
	Description *string       `json:"description,omitempty" jsonschema:"New description. Omit to keep."`
	Steps       []api.StepDef `json:"steps,omitempty" jsonschema:"The whole new step list, in order: each {id, kind?, title?, params?}; kinds from the orchestrator's ability catalog. Omit to keep."`
}

// RunbookEdit changes an adopted editable copy on gears-api. A frozen copy
// is refused here first (from the cache), then by gears-api.
func RunbookEdit(c Ctx, in RunbookEditIn) (*api.AdoptedRunbook, error) {
	slug := strings.TrimSpace(in.Runbook)
	if slug == "" {
		return nil, errors.New("runbook required: the adopted runbook's slug")
	}
	if rb, _ := runs.Runbook(c.Project, slug); rb != nil && rb.Frozen {
		return nil, runs.RefuseAdopted(c.Project, slug)
	}
	ws, err := c.runsWorkspace()
	if err != nil {
		return nil, err
	}
	cl, err := runsClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := runCtx()
	defer cancel()
	out, err := cl.EditRunbook(ctx, ws, slug, api.RunbookEditIn{Title: in.Title, Description: in.Description, Steps: in.Steps})
	if err != nil {
		return nil, err
	}
	_ = runs.SaveRunbook(c.Project, *out)
	return out, nil
}

type RunbookAbilitiesIn struct{}

// RunbookAbilities is the step-kind catalog (GRS-70): byz-orchestrator's
// abilities through gears-api. Needs gears login and the runbook service.
func RunbookAbilities(c Ctx, _ RunbookAbilitiesIn) (*api.Abilities, error) {
	ws, err := c.runsWorkspace()
	if err != nil {
		return nil, err
	}
	cl, err := runsClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := runCtx()
	defer cancel()
	out, err := cl.RunbookAbilities(ctx, ws)
	if err != nil {
		return nil, err
	}
	if out.Items == nil {
		out.Items = []api.Ability{}
	}
	return out, nil
}

type RunbookUnadoptIn struct {
	Runbook string `json:"runbook" jsonschema:"The adopted runbook's slug (frozen or editable). Runs already started keep their steps."`
}

type RunbookUnadoptOut struct {
	Runbook string `json:"runbook"`
	Deleted bool   `json:"deleted"`
}

// RunbookUnadopt removes an adopted copy on gears-api (GRS-73), and from
// the local cache. It needs no orchestrator.
func RunbookUnadopt(c Ctx, in RunbookUnadoptIn) (*RunbookUnadoptOut, error) {
	slug := strings.TrimSpace(in.Runbook)
	if slug == "" {
		return nil, errors.New("runbook required: the adopted runbook's slug")
	}
	ws, err := c.runsWorkspace()
	if err != nil {
		return nil, err
	}
	cl, err := runsClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := runCtx()
	defer cancel()
	if err := cl.DeleteRunbook(ctx, ws, slug); err != nil {
		return nil, err
	}
	if rb, _ := runs.Runbook(c.Project, slug); rb != nil {
		_ = runs.ForgetRunbook(c.Project, rb.ID)
	}
	return &RunbookUnadoptOut{Runbook: slug, Deleted: true}, nil
}
