package control

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/iam"
	"github.com/krun-dev/krun/internal/securetoken"
)

func (s *Server) organizationInvitationRoutes(m *http.ServeMux) {
	base := "/api/v1/orgs/{org}/invitation-links"
	m.HandleFunc("GET "+base, s.organizationInvitationManage)
	m.HandleFunc("POST "+base, s.organizationInvitationManage)
	m.HandleFunc("DELETE "+base+"/{invitation}", s.organizationInvitationManage)
	m.HandleFunc("POST /api/v1/invitation-links/preview", s.organizationInvitationPreview)
	m.HandleFunc("POST /api/v1/invitation-links/accept", s.organizationInvitationAccept)
}

func (s *Server) invitationCredentials(r *http.Request) (authCredentials, error) {
	c, err := s.readCredentials(r)
	if err != nil {
		return c, err
	}
	if !c.hasSession {
		if c.keyID != "" {
			return c, forbidden()
		}
		return c, problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	return c, nil
}

func (s *Server) invitationPrincipal(r *http.Request, c authCredentials, org string, extra ...string) (*State, Principal, error) {
	users := append([]string{c.session.User}, extra...)
	st, err := s.Store.OrganizationInvitationContext(r.Context(), org, users...)
	if err != nil {
		return nil, Principal{}, err
	}
	p, err := s.protectedPrincipal(st, c, r, true)
	return st, p, err
}

func invitationAudit(p Principal, r *http.Request, org, id, action string) domain.Audit {
	return domain.Audit{
		ID: securetoken.NewID("audit"), Time: now(), Actor: p.User.ID, Credential: p.Kind,
		Action: "organization.invitation." + action, Resource: id, Organization: org,
		Allowed: true, RequestID: r.Header.Get("X-Krun-Request-ID"),
	}
}

func invitationRevisionConflict(err error) bool {
	var problem *domain.APIError
	return errors.As(err, &problem) && problem.Code == "REVISION_CONFLICT"
}

func (s *Server) organizationInvitationManage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c, err := s.invitationCredentials(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err = s.resolveNativeRoute(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var input iam.OrganizationInvitationInput
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, apiJSONBodyMax)
		if err := body(r, &input); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	org := r.PathValue("org")
	for attempt := 0; attempt < 5; attempt++ {
		st, p, err := s.invitationPrincipal(r, c, org, strings.TrimSpace(input.TargetUser))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		target, err := s.orgTarget(st, r)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		actions := []string{"member.read"}
		if r.Method != http.MethodGet {
			actions = []string{"member.manage", "iam.grant"}
		}
		for _, action := range actions {
			err = s.require(st, p, r, action, target)
			if err != nil {
				s.writeError(w, r, err)
				return
			}
		}
		switch r.Method {
		case http.MethodGet:
			s.listOrganizationInvitations(w, r, org)
			return
		case http.MethodDelete:
			id := r.PathValue("invitation")
			err = s.Store.RevokeOrganizationInvitation(r.Context(), org, id, invitationAudit(p, r, org, id, "revoke"))
			if err != nil {
				s.writeError(w, r, err)
				return
			}
			s.json(w, http.StatusNoContent, nil)
			return
		case http.MethodPost:
			in, token, createErr := s.iam().NewOrganizationInvitation(st, org, p.User.ID, input, now())
			if createErr != nil {
				s.writeError(w, r, createErr)
				return
			}
			err = s.Store.CreateOrganizationInvitation(r.Context(), in, st.Organizations[org].Revision, invitationAudit(p, r, org, in.ID, "create"))
			if invitationRevisionConflict(err) {
				continue
			}
			if err != nil {
				s.writeError(w, r, err)
				return
			}
			s.json(w, http.StatusCreated, map[string]any{
				"invitation": in,
				"invite_url": strings.TrimRight(s.Config.PublicURL, "/") + "/?page=join#invite=" + token,
			})
			return
		}
	}
	s.writeError(w, r, problem(409, "REVISION_CONFLICT", "Invitation changed; retry the request"))
}

func (s *Server) listOrganizationInvitations(w http.ResponseWriter, r *http.Request, org string) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			s.writeError(w, r, problem(422, "INVALID_REQUEST", "Invitation page limit must be between 1 and 100"))
			return
		}
		limit = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 128 {
		s.writeError(w, r, problem(422, "INVALID_REQUEST", "Invalid invitation cursor"))
		return
	}
	items, err := s.Store.ListOrganizationInvitations(r.Context(), org, cursor, limit+1)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	s.json(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func invitationToken(w http.ResponseWriter, r *http.Request) (string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, apiJSONBodyMax)
	var input struct {
		Token string `json:"token"`
	}
	if err := body(r, &input); err != nil {
		return "", err
	}
	if !strings.HasPrefix(input.Token, "krun_invite_") || len(input.Token) != len("krun_invite_")+43 {
		return "", iam.InvitationUnavailable()
	}
	return input.Token, nil
}

func (s *Server) organizationInvitationPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	token, err := invitationToken(w, r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	in, found, err := s.Store.OrganizationInvitation(r.Context(), "", digest(token))
	if err == nil && !found {
		err = iam.InvitationUnavailable()
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	st, err := s.Store.OrganizationInvitationContext(r.Context(), in.Organization, in.Creator)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	preview, err := s.iam().PreviewOrganizationInvitation(st, in, now())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, http.StatusOK, preview)
}

func (s *Server) organizationInvitationAccept(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c, err := s.invitationCredentials(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if c.session.CLI {
		s.writeError(w, r, forbidden())
		return
	}
	token, err := invitationToken(w, r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	for attempt := 0; attempt < 5; attempt++ {
		in, found, err := s.Store.OrganizationInvitation(r.Context(), "", digest(token))
		if err == nil && !found {
			err = iam.InvitationUnavailable()
		}
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		// Read redemption first: a committed redemption must be followed by a
		// fresh membership read, including concurrent retries by the same user.
		redeemed, err := s.Store.OrganizationInvitationRedeemed(r.Context(), in.ID, c.session.User)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		st, p, err := s.invitationPrincipal(r, c, in.Organization, in.Creator)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		result, assignment, err := s.iam().AcceptOrganizationInvitation(st, in, p, redeemed, now())
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if result.Status == "already_member" {
			s.json(w, http.StatusOK, result)
			return
		}
		joined, err := s.Store.RedeemOrganizationInvitation(r.Context(), in, p.User, assignment, invitationAudit(p, r, in.Organization, in.ID, "accept"))
		if invitationRevisionConflict(err) || err == nil && !joined {
			continue
		}
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.json(w, http.StatusOK, result)
		return
	}
	s.writeError(w, r, problem(409, "REVISION_CONFLICT", "Invitation changed; retry the request"))
}
