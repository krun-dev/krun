package iam

import (
	"net/mail"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

const OrganizationInvitationLifetime = 7 * 24 * time.Hour

type OrganizationInvitationInput struct {
	Role        string `json:"role_id"`
	MaxUses     *int   `json:"max_uses,omitempty"`
	TargetUser  string `json:"target_user_id,omitempty"`
	TargetEmail string `json:"target_email,omitempty"`
}

func (s Service) NewOrganizationInvitation(st *domain.State, org, creator string, in OrganizationInvitationInput, at time.Time) (domain.OrganizationInvitation, string, error) {
	if _, ok := st.Organizations[org]; !ok {
		return domain.OrganizationInvitation{}, "", domain.NotFound()
	}
	if in.Role == "" {
		in.Role = "member"
	}
	if in.Role != "member" && in.Role != "viewer" {
		return domain.OrganizationInvitation{}, "", domain.Invalid("INVALID_REQUEST", "Invitation role must be member or viewer")
	}
	maxUses := 1
	if in.MaxUses != nil {
		maxUses = *in.MaxUses
	}
	if maxUses < 1 || maxUses > 100 {
		return domain.OrganizationInvitation{}, "", domain.Invalid("INVALID_REQUEST", "Invitation limit must be between 1 and 100")
	}
	in.TargetUser = strings.TrimSpace(in.TargetUser)
	in.TargetEmail = strings.ToLower(strings.TrimSpace(in.TargetEmail))
	if in.TargetUser != "" && in.TargetEmail != "" {
		return domain.OrganizationInvitation{}, "", domain.Invalid("INVALID_REQUEST", "Choose one invitation recipient")
	}
	if in.TargetUser != "" {
		user, ok := st.Users[in.TargetUser]
		if !ok || user.Status != "active" {
			return domain.OrganizationInvitation{}, "", domain.Invalid("INVALID_REQUEST", "Existing active invitation recipient required")
		}
	}
	if in.TargetEmail != "" {
		address, err := mail.ParseAddress(in.TargetEmail)
		if err != nil || address.Address != in.TargetEmail || len(in.TargetEmail) > 254 {
			return domain.OrganizationInvitation{}, "", domain.Invalid("INVALID_REQUEST", "Valid invitation email required")
		}
	}
	token := "krun_invite_" + securetoken.Random()
	invitation := domain.OrganizationInvitation{
		ID:           securetoken.NewID("inv"),
		Organization: org,
		Creator:      creator,
		Role:         in.Role,
		TargetUser:   in.TargetUser,
		TargetEmail:  in.TargetEmail,
		MaxUses:      maxUses,
		Created:      at,
		Expires:      at.Add(OrganizationInvitationLifetime),
		TokenHash:    securetoken.Digest(token),
		Revision:     1,
	}
	return invitation.WithStatus(at), token, nil
}

func InvitationUnavailable() error {
	return domain.Problem(410, "INVITATION_UNAVAILABLE", "Invitation link is unavailable")
}

func (s Service) invitationAvailable(st *domain.State, in domain.OrganizationInvitation, at time.Time) error {
	creator, ok := st.Users[in.Creator]
	if !ok || creator.Status != "active" || !s.sourceEnabled(creator.Source) || in.Revoked != nil || !at.Before(in.Expires) {
		return InvitationUnavailable()
	}
	target, err := authorization.Resolve(st, "organization", in.Organization, "global")
	if err != nil {
		return InvitationUnavailable()
	}
	for _, action := range []string{"member.manage", "iam.grant"} {
		decision, err := authorization.Authorize(st, authorization.Principal{User: creator}, action, target)
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return InvitationUnavailable()
		}
	}
	return nil
}

func (s Service) PreviewOrganizationInvitation(st *domain.State, in domain.OrganizationInvitation, at time.Time) (domain.OrganizationInvitationPreview, error) {
	if err := s.invitationAvailable(st, in, at); err != nil {
		return domain.OrganizationInvitationPreview{}, err
	}
	if in.UseCount >= in.MaxUses {
		return domain.OrganizationInvitationPreview{}, InvitationUnavailable()
	}
	return domain.OrganizationInvitationPreview{
		OrganizationName: st.Organizations[in.Organization].Name,
		Role:             in.Role,
		Expires:          in.Expires,
		Targeted:         in.TargetUser != "" || in.TargetEmail != "",
	}, nil
}

// AcceptOrganizationInvitation validates a request without mutating its snapshot.
// Storage commits the member, role, redemption and bounded-use CAS atomically.
func (s Service) AcceptOrganizationInvitation(st *domain.State, in domain.OrganizationInvitation, p authorization.Principal, redeemed bool, at time.Time) (domain.OrganizationInvitationResult, domain.Assignment, error) {
	result := domain.OrganizationInvitationResult{Organization: in.Organization}
	if p.Kind != "session" || p.User.Status != "active" || !s.sourceEnabled(p.User.Source) {
		return result, domain.Assignment{}, domain.Forbidden()
	}
	if err := s.invitationAvailable(st, in, at); err != nil {
		return result, domain.Assignment{}, err
	}
	if !authorization.SourceAllowed(st, p.User, in.Organization) {
		return result, domain.Assignment{}, domain.ForbiddenCode("INVITATION_SOURCE_DENIED", "Account identity source is not allowed by this organization")
	}
	if in.TargetUser != "" && in.TargetUser != p.User.ID || in.TargetEmail != "" && (!p.User.EmailVerified || !strings.EqualFold(strings.TrimSpace(p.User.Email), in.TargetEmail)) {
		return result, domain.Assignment{}, domain.ForbiddenCode("INVITATION_RECIPIENT_MISMATCH", "Current account is not the invitation recipient")
	}
	if _, exists := st.Members[domain.MemberKey(in.Organization, p.User.ID)]; exists {
		result.Status = "already_member"
		target, _ := authorization.Resolve(st, "organization", in.Organization, "global")
		decision, err := authorization.Authorize(st, p, "organization.read", target)
		result.CanEnter = decision.Allowed
		return result, domain.Assignment{}, err
	}
	if redeemed {
		return result, domain.Assignment{}, domain.Conflict("INVITATION_ALREADY_USED", "A new invitation is required after membership removal")
	}
	if in.UseCount >= in.MaxUses {
		return result, domain.Assignment{}, InvitationUnavailable()
	}
	result.Status = "joined"
	result.CanEnter = true
	assignment := domain.Assignment{
		ID:          securetoken.NewID("role"),
		SubjectKind: "user",
		SubjectID:   p.User.ID,
		Role:        in.Role,
		ScopeKind:   "organization",
		ScopeID:     in.Organization,
		Revision:    1,
	}
	return result, assignment, nil
}
