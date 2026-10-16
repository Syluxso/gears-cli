package fakeapi

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Syluxso/gears-cli/internal/api"
)

// Official and adopted runbooks (GRS-59) on an in-memory model: the
// catalog has single-ticket-development (frozen) and starter (editable).
// Copies land in the pull log as table runbooks.

var (
	reWsRunbooks = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)/runbooks(?:/([^/]+))?(/detail)?$`)
)

func fakeOfficial() []api.OfficialRunbook {
	return []api.OfficialRunbook{
		{Ref: "single-ticket-development", Version: 1, Title: "Single ticket development", Frozen: true,
			Steps: []api.StepDef{{ID: "take", Kind: "card.move"}, {ID: "push", Kind: "git.push"}}},
		{Ref: "starter", Version: 1, Title: "Starter runbook",
			Steps: []api.StepDef{{ID: "plan", Kind: "ask"}, {ID: "log", Kind: "note"}}},
	}
}

func (s *Server) adoptedRunbooks() []api.AdoptedRunbook {
	out := []api.AdoptedRunbook{}
	for _, raw := range s.rows["runbooks"] {
		var rb api.AdoptedRunbook
		if raw != nil && json.Unmarshal(raw, &rb) == nil {
			out = append(out, rb)
		}
	}
	return out
}

func (s *Server) runbooks(w http.ResponseWriter, r *http.Request) bool {
	m := reWsRunbooks.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	ws, key := m[1], m[2]
	find := func(slug string) *api.AdoptedRunbook {
		for _, rb := range s.adoptedRunbooks() {
			if strings.EqualFold(rb.Slug, slug) {
				rb := rb
				return &rb
			}
		}
		return nil
	}
	save := func(rb *api.AdoptedRunbook) {
		b, _ := json.Marshal(rb)
		s.put("runbooks", rb.ID, b, "upsert", "tester")
	}
	switch {
	case m[3] != "" && r.Method == http.MethodGet:
		s.runbookDetail(w, key, find(key), ok)
	case key == "abilities" && r.Method == http.MethodGet:
		if s.RunbooksOff {
			problem(w, 501, "gears.runbooks_unavailable", "runbooks are not available on this gears-api")
			return true
		}
		ok(200, fakeAbilities())
	case key == "" && r.Method == http.MethodGet:
		ok(200, map[string]any{"items": s.adoptedRunbooks()})
	case key == "official" && r.Method == http.MethodGet:
		if s.RunbooksOff {
			problem(w, 501, "gears.runbooks_unavailable", "runbooks are not available on this gears-api")
			return true
		}
		list := fakeOfficial()
		for i := range list {
			list[i].Adopted = []string{}
			for _, rb := range s.adoptedRunbooks() {
				if rb.SourceRef == list[i].Ref {
					list[i].Adopted = append(list[i].Adopted, rb.Slug)
				}
			}
		}
		ok(200, map[string]any{"items": list})
	case key == "adopt" && r.Method == http.MethodPost:
		var in struct{ Ref, As string }
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		var off *api.OfficialRunbook
		for _, o := range fakeOfficial() {
			if o.Ref == in.Ref {
				o := o
				off = &o
			}
		}
		if off == nil {
			problem(w, 404, "gears.not_found", "no official runbook "+in.Ref)
			return true
		}
		slug := off.Ref
		if !off.Frozen {
			slug += "-copy"
		}
		if in.As != "" {
			slug = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(in.As), " ", "-"))
		}
		if find(slug) != nil {
			problem(w, 409, "gears.runbook_exists", slug+" is already a runbook in this workspace")
			return true
		}
		now := time.Now().UTC()
		rb := &api.AdoptedRunbook{ID: uuid.NewString(), WorkspaceID: ws, Slug: slug, Title: off.Title, SourceRef: off.Ref,
			SourceVersion: off.Version, Frozen: off.Frozen, Steps: off.Steps, CreatedAt: now, UpdatedAt: now}
		save(rb)
		ok(201, rb)
	case r.Method == http.MethodPut:
		rb := find(key)
		if rb == nil {
			problem(w, 404, "gears.not_found", "not found")
			return true
		}
		if rb.Frozen {
			problem(w, 409, "gears.runbook_frozen", rb.Slug+" is a frozen copy of the official runbook "+rb.SourceRef+" and cannot be edited")
			return true
		}
		var in api.RunbookEditIn
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		if in.Title != nil {
			rb.Title = *in.Title
		}
		if in.Description != nil {
			rb.Description = *in.Description
		}
		if in.Steps != nil {
			rb.Steps = in.Steps
		}
		rb.UpdatedAt = time.Now().UTC()
		save(rb)
		ok(200, rb)
	case r.Method == http.MethodGet:
		rb := find(key)
		if rb == nil {
			problem(w, 404, "gears.not_found", "not found")
			return true
		}
		ok(200, rb)
	default:
		problem(w, 405, "gears.bad_request", "method")
	}
	return true
}

// fakeAbilities is the step-kind catalog (GRS-70): a few kinds.
func fakeAbilities() api.Abilities {
	return api.Abilities{Items: []api.Ability{
		{Kind: "card.move", Title: "Move a card", Tool: "card_move", Via: "mcp", EvidenceKinds: []string{"card"}, RequiresEvidence: true,
			Params: []api.AbilityParam{{Name: "state", Type: "string", Required: true}}},
		{Kind: "ask", Title: "Ask a person", Tool: "desk_send", Via: "mcp", EvidenceKinds: []string{"desk"}, RequiresEvidence: true,
			Params: []api.AbilityParam{{Name: "question", Type: "string", Required: true}}},
		{Kind: "note", Title: "Log a note", Tool: "session_new", Via: "mcp", EvidenceKinds: []string{"session"}, Params: []api.AbilityParam{}},
	}, EvidenceKinds: []string{"session", "artifact", "commit", "desk", "card"}}
}

// runbookDetail answers GET .../runbooks/:name/detail: the adopted copy,
// else the official one, steps explained from fakeAbilities.
func (s *Server) runbookDetail(w http.ResponseWriter, name string, ad *api.AdoptedRunbook, ok func(int, any)) {
	var d *api.RunbookDetail
	var steps []api.StepDef
	if ad != nil {
		d = &api.RunbookDetail{Runbook: ad.Slug, Source: "adopted", Title: ad.Title, Frozen: ad.Frozen, Editable: !ad.Frozen,
			SourceRef: ad.SourceRef, SourceVersion: ad.SourceVersion}
		steps = ad.Steps
	} else if !s.RunbooksOff {
		for _, o := range fakeOfficial() {
			if o.Ref == name {
				d = &api.RunbookDetail{Runbook: o.Ref, Source: "official", Title: o.Title, Frozen: o.Frozen, SourceRef: o.Ref, SourceVersion: o.Version}
				steps = o.Steps
			}
		}
	}
	if d == nil {
		problem(w, 404, "gears.not_found", "no adopted or official runbook "+name)
		return
	}
	by := map[string]api.Ability{}
	for _, a := range fakeAbilities().Items {
		by[a.Kind] = a
	}
	d.AbilitiesAvailable = !s.RunbooksOff
	if s.RunbooksOff {
		d.Note = "the step-kind catalog could not be read"
	}
	d.Steps = []api.RunbookDetailStep{}
	for _, st := range steps {
		ds := api.RunbookDetailStep{ID: st.ID, Kind: st.Kind, Title: st.Title, Params: st.Params}
		if a, found := by[st.Kind]; found && !s.RunbooksOff {
			ds.Ability, ds.Tool, ds.EvidenceKinds, ds.RequiresEvidence, ds.ParamSpec = a.Title, a.Tool, a.EvidenceKinds, a.RequiresEvidence, a.Params
		} else if !s.RunbooksOff {
			ds.Unknown = true
		}
		d.Steps = append(d.Steps, ds)
	}
	ok(200, d)
}
