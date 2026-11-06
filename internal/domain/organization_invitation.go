package domain

import "time"

// OrganizationInvitation is public metadata; its token digest is never serialized.
type OrganizationInvitation struct {
	ID           string     `json:"id"`
	Organization string     `json:"organization_id"`
	Creator      string     `json:"creator_user_id"`
	Role         string     `json:"role_id"`
	TargetUser   string     `json:"target_user_id,omitempty"`
	TargetEmail  string     `json:"target_email,omitempty"`
	MaxUses      int        `json:"max_uses"`
	UseCount     int        `json:"use_count"`
	Created      time.Time  `json:"created_at"`
	Expires      time.Time  `json:"expires_at"`
	Revoked      *time.Time `json:"revoked_at"`
	Status       string     `json:"status"`
	TokenHash    string     `json:"-"`
	Revision     int        `json:"-"`
}

func (in OrganizationInvitation) WithStatus(at time.Time) OrganizationInvitation {
	switch {
	case in.Revoked != nil:
		in.Status = "revoked"
	case !at.Before(in.Expires):
		in.Status = "expired"
	case in.UseCount >= in.MaxUses:
		in.Status = "exhausted"
	default:
		in.Status = "active"
	}
	return in
}

type OrganizationInvitationPreview struct {
	OrganizationName string    `json:"organization_name"`
	Role             string    `json:"role_id"`
	Expires          time.Time `json:"expires_at"`
	Targeted         bool      `json:"targeted"`
}

type OrganizationInvitationResult struct {
	Organization string `json:"organization_id"`
	Status       string `json:"status"`
	CanEnter     bool   `json:"can_enter"`
}
