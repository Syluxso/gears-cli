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
	reWsRunbooks = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)/runbooks(?:/([^/]+))?$`)
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
	case key == "" && r.Method == http.MethodGet:
		ok(200, map[string]any{"items": s.adoptedRunbooks()})
	case key == "official" && r.Method == http.MethodGet:
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
