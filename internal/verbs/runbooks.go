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

type RunbookCatalogIn struct{}

type RunbookCatalogOut struct {
	Official []api.OfficialRunbook `json:"official"`
	Adopted  []api.AdoptedRunbook  `json:"adopted"`
}

// RunbookCatalog lists the official runbooks (live) and the workspace's
// adopted copies, caching the copies.
func RunbookCatalog(c Ctx, _ RunbookCatalogIn) (*RunbookCatalogOut, error) {
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
	off, err := cl.OfficialRunbooks(ctx, ws)
	if err != nil {
		return nil, err
	}
	mine, err := cl.AdoptedRunbooks(ctx, ws)
	if err != nil {
		return nil, err
	}
	for _, rb := range mine {
		_ = runs.SaveRunbook(c.Project, rb)
	}
	if off == nil {
		off = []api.OfficialRunbook{}
	}
	if mine == nil {
		mine = []api.AdoptedRunbook{}
	}
	return &RunbookCatalogOut{Official: off, Adopted: mine}, nil
}

type RunbookAdoptIn struct {
	Ref string `json:"ref" jsonschema:"The official runbook to adopt (runbook_catalog official[].ref), e.g. single-ticket-development."`
	As  string `json:"as,omitempty" jsonschema:"Name for the copy (its slug is derived). Default: the ref for a frozen runbook, <ref>-copy for an editable one."`
}

// RunbookAdopt copies an official runbook into the workspace on gears-api.
func RunbookAdopt(c Ctx, in RunbookAdoptIn) (*api.AdoptedRunbook, error) {
	if strings.TrimSpace(in.Ref) == "" {
		return nil, errors.New("ref required: an official runbook (gears runbook catalog)")
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
