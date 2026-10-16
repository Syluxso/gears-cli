package fakeapi

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
)

// Workspace soft delete and restore (GRS-80): the one workspace this fake
// serves (WSID, WSSlug), deleted or not. Deleted, its sync, board and board
// stream answer 410 gears.workspace_deleted; DELETE needs confirm = the
// slug and an owner (MeRole); restore clears it.

var (
	reWsOne     = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)$`)
	reWsRestore = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)/restore$`)
)

// SetWorkspace names the workspace the fake serves.
func (s *Server) SetWorkspace(id, slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.WSID, s.WSSlug = id, slug
}

// SetDeleted marks the workspace deleted (as another client's delete
// would) and, deleted, tells every open stream workspace.deleted and ends it.
func (s *Server) SetDeleted(deleted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setDeleted(deleted, "elsewhere")
}

func (s *Server) setDeleted(deleted bool, by string) {
	s.Deleted = deleted
	if !deleted {
		s.DeletedAt, s.DeletedBy = time.Time{}, ""
		return
	}
	s.DeletedAt, s.DeletedBy = time.Now().UTC(), by
	for ch := range s.subs {
		select {
		case ch <- "event: workspace.deleted\ndata: {\"type\":\"workspace.deleted\"}\n\n" + streamEnd:
		default:
		}
	}
}

// streamEnd tells stream() to return after writing.
const streamEnd = "\x00end"

func (s *Server) gone(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusGone)
	_ = json.NewEncoder(w).Encode(api.Problem{Status: http.StatusGone, Code: api.CodeWorkspaceDeleted,
		Detail: "workspace " + s.WSSlug + " was deleted; its owner can restore it"})
}

func (s *Server) problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Problem{Status: status, Code: code, Detail: detail})
}

// deletedGate answers 410 for the workspace's sync and board routes while
// it is deleted; true when it answered.
func (s *Server) deletedGate(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Deleted {
		return false
	}
	p := r.URL.Path
	if strings.HasSuffix(p, "/sync") || strings.HasSuffix(p, "/board") || strings.HasSuffix(p, "/board/events") {
		s.gone(w)
		return true
	}
	return false
}

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) bool {
	path, m := r.URL.Path, r.Method
	if path != "/api/v1/workspaces" && !reWsOne.MatchString(path) && !reWsRestore.MatchString(path) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	me := api.Workspace{ID: s.WSID, Slug: s.WSSlug, Prefix: s.Prefix}
	switch {
	case m == http.MethodGet && path == "/api/v1/workspaces":
		if r.URL.Query().Get("deleted") == "true" {
			out := []api.DeletedWorkspace{}
			if s.Deleted && s.owner() {
				out = append(out, api.DeletedWorkspace{ID: s.WSID, Slug: s.WSSlug, DeletedAt: s.DeletedAt, DeletedBy: s.DeletedBy})
			}
			ok(out)
			return true
		}
		out := []api.Workspace{}
		if !s.Deleted {
			out = append(out, me)
		}
		ok(out)
	case m == http.MethodGet && reWsOne.MatchString(path):
		id := reWsOne.FindStringSubmatch(path)[1]
		switch {
		case !strings.EqualFold(id, s.WSID):
			s.problem(w, 404, "gears.not_found", "not found")
		case s.Deleted && s.owner():
			s.gone(w)
		case s.Deleted:
			s.problem(w, 404, "gears.not_found", "not found")
		default:
			ok(me)
		}
	case m == http.MethodDelete && reWsOne.MatchString(path):
		id := reWsOne.FindStringSubmatch(path)[1]
		switch {
		case !strings.EqualFold(id, s.WSID) || s.Deleted:
			s.problem(w, 404, "gears.not_found", "not found")
		case !s.owner():
			s.problem(w, 403, "gears.forbidden", "only a workspace owner may do that")
		case !strings.EqualFold(r.URL.Query().Get("confirm"), s.WSSlug):
			s.problem(w, 400, "gears.bad_request", "confirm does not match the workspace slug ("+s.WSSlug+")")
		default:
			by := r.Header.Get("X-Gears-Label")
			if by == "" {
				by = "tester"
			}
			s.setDeleted(true, by)
			w.WriteHeader(http.StatusNoContent)
		}
	case m == http.MethodPost && reWsRestore.MatchString(path):
		key := reWsRestore.FindStringSubmatch(path)[1]
		switch {
		case !strings.EqualFold(key, s.WSID) && !strings.EqualFold(key, s.WSSlug):
			s.problem(w, 404, "gears.not_found", "not found")
		case !s.owner():
			s.problem(w, 403, "gears.forbidden", "only a workspace owner may do that")
		default:
			s.setDeleted(false, "")
			ok(me)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "method not allowed")
	}
	return true
}
