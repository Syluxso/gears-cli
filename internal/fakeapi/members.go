package fakeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Syluxso/gears-cli/internal/api"
)

var (
	reMembers  = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)/members$`)
	reMember   = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)/members/([^/]+)$`)
	reWsInv    = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)/invites$`)
	reWsInvOne = regexp.MustCompile(`^/api/v1/workspaces/([^/]+)/invites/([^/]+)$`)
	reRespond  = regexp.MustCompile(`^/api/v1/invites/([^/]+)/(accept|decline)$`)
)

func problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "detail": detail})
}

func (s *Server) owner() bool { return s.MeRole == "" || s.MeRole == "owner" }

// membership answers the GRS-34 routes the way gears-api does (members.go),
// for one workspace and one caller. False when the path is not one of them.
func (s *Server) membership(w http.ResponseWriter, r *http.Request) bool {
	path, m := r.URL.Path, r.Method
	isMember := reMembers.MatchString(path) || reMember.MatchString(path) || reWsInv.MatchString(path) ||
		reWsInvOne.MatchString(path) || reRespond.MatchString(path) || path == "/api/v1/invites"
	if !isMember {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.MembersStatus != 0 {
		w.WriteHeader(s.MembersStatus)
		_, _ = io.WriteString(w, "404 page not found")
		return true
	}
	ok := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case m == http.MethodGet && reMembers.MatchString(path):
		out := []api.Member{}
		for _, x := range s.Members {
			if x.Status == "active" || r.URL.Query().Get("all") == "true" {
				out = append(out, x)
			}
		}
		ok(out)
	case m == http.MethodDelete && reMember.MatchString(path):
		if !s.owner() {
			problem(w, 403, "gears.forbidden", "only a workspace owner may do that")
			return true
		}
		uid := reMember.FindStringSubmatch(path)[2]
		for i := range s.Members {
			if s.Members[i].UserID == uid && s.Members[i].Status == "active" {
				if s.Members[i].Role == "owner" {
					problem(w, 400, "gears.bad_request", "an owner cannot be removed")
					return true
				}
				s.Members[i].Status = "removed"
				ok(s.Members[i])
				return true
			}
		}
		problem(w, 404, "gears.not_found", "not found")
	case m == http.MethodGet && reWsInv.MatchString(path):
		out := []api.Invite{}
		for _, iv := range s.Invites {
			if iv.Status == "pending" && iv.WorkspaceID == reWsInv.FindStringSubmatch(path)[1] {
				out = append(out, iv)
			}
		}
		ok(out)
	case m == http.MethodPost && reWsInv.MatchString(path):
		var in struct{ Email, UserID, Role string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		role := in.Role
		if role == "" {
			role = "member"
		}
		if role != "member" && role != "owner" {
			problem(w, 400, "gears.bad_request", "role is owner or member")
			return true
		}
		if role == "owner" && !s.owner() {
			problem(w, 403, "gears.forbidden", "only a workspace owner may do that")
			return true
		}
		uid := s.Accounts[strings.ToLower(in.Email)]
		if in.UserID != "" {
			uid = in.UserID
		}
		if uid == "" {
			problem(w, 404, "gears.no_account", "no Gears account with that email or id; they must sign up first")
			return true
		}
		for _, x := range s.Members {
			if x.UserID == uid && x.Status == "active" {
				problem(w, 409, "gears.already_member", "that account is already a member of this workspace")
				return true
			}
		}
		for _, iv := range s.Invites {
			if iv.InviteeID == uid && iv.Status == "pending" {
				problem(w, 409, "gears.already_invited", "that account already has a pending invite to this workspace")
				return true
			}
		}
		s.inviteSeq++
		iv := api.Invite{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", s.inviteSeq), WorkspaceID: reWsInv.FindStringSubmatch(path)[1],
			WorkspaceSlug: "ws", WorkspaceName: "WS", InviteeID: uid, InviteeEmail: in.Email, Role: role, Status: "pending", InvitedBy: s.Me, CreatedAt: time.Now().UTC()}
		s.Invites = append(s.Invites, iv)
		w.WriteHeader(http.StatusCreated)
		ok(iv)
	case m == http.MethodDelete && reWsInvOne.MatchString(path):
		id := reWsInvOne.FindStringSubmatch(path)[2]
		for i := range s.Invites {
			if s.Invites[i].ID == id && s.Invites[i].Status == "pending" {
				if !s.owner() && s.Invites[i].InvitedBy != s.Me {
					problem(w, 403, "gears.forbidden", "only a workspace owner may do that")
					return true
				}
				s.Invites[i].Status = "revoked"
				ok(s.Invites[i])
				return true
			}
		}
		problem(w, 404, "gears.not_found", "not found")
	case m == http.MethodGet && path == "/api/v1/invites":
		out := []api.Invite{}
		for _, iv := range s.Invites {
			if iv.InviteeID == s.Me && iv.Status == "pending" {
				out = append(out, iv)
			}
		}
		ok(out)
	case m == http.MethodPost && reRespond.MatchString(path):
		sm := reRespond.FindStringSubmatch(path)
		for i := range s.Invites {
			iv := &s.Invites[i]
			if iv.ID == sm[1] && iv.InviteeID == s.Me && iv.Status == "pending" {
				iv.Status = sm[2] + "d"
				if sm[2] == "accept" {
					iv.Status = "accepted"
					s.Members = append(s.Members, api.Member{UserID: s.Me, Role: iv.Role, Status: "active", InvitedBy: iv.InvitedBy})
				}
				ok(iv)
				return true
			}
		}
		problem(w, 404, "gears.not_found", "not found")
	default:
		problem(w, 405, "gears.method", "method not allowed")
	}
	return true
}
