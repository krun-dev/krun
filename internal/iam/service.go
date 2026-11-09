// Package iam implements IAM administration over a request's working copy.
// Callers resolve and authorize the target before invoking an operation, and
// discard the working copy on error. Storage owns atomic persistence and CAS;
// this package never performs I/O or shares a transaction with Workers.
package iam

import (
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
)

type Service struct{ sourceEnabled func(string) bool }

// NewService receives the enabled-source check from the application's validated
// configuration. It must be a local lookup, not a network call.
func NewService(sourceEnabled func(string) bool) Service {
	return Service{sourceEnabled: sourceEnabled}
}
func nameValid(name string) bool { return len(name) > 0 && len(name) <= 128 }

type NameInput struct {
	Name string `json:"name"`
}
type OrganizationCreateInput struct {
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
}

type ResourceCreateInput struct {
	Name    string `json:"name"`
	Locator string `json:"locator"`
}

func firstName(preferred, name string) string {
	if preferred != "" {
		return preferred
	}
	return name
}

type RevisionNameInput struct {
	Name     string `json:"name"`
	Revision int    `json:"expected_revision"`
}
type OrganizationInput struct {
	Name     string   `json:"name"`
	Sources  []string `json:"allowed_sources"`
	Revision int      `json:"expected_revision"`
}
type MemberInput struct {
	User string `json:"user_id"`
}
type UserInput struct {
	Status string `json:"status"`
}
type InvitationInput struct {
	Source  string `json:"source_id"`
	Subject string `json:"subject"`
}
type AssignmentInput struct {
	SubjectKind string            `json:"subject_kind"`
	SubjectID   string            `json:"subject_id"`
	Role        string            `json:"role_id"`
	ScopeKind   string            `json:"scope_kind"`
	ScopeID     string            `json:"scope_id"`
	Conditions  domain.Conditions `json:"conditions"`
	Revision    int               `json:"expected_revision,omitempty"`
}
type KeyInput struct {
	Name    string        `json:"name"`
	Expires *int          `json:"expires_in"`
	Policy  domain.Policy `json:"policy"`
}
type KeyUpdateInput struct {
	Name     string        `json:"name"`
	Policy   domain.Policy `json:"policy"`
	Revision int           `json:"expected_revision"`
}
type CreatedKey struct {
	ID             string        `json:"id"`
	Key            string        `json:"key"`
	Metadata       domain.APIKey `json:"metadata"`
	PolicyRevision int           `json:"policy_revision"`
	Expires        *time.Time    `json:"expires_at"`
}

func (s Service) Group(st *domain.State, t authorization.Target, id string) (domain.Group, error) {
	g, ok := st.Groups[id]
	if !ok || !sameScope(g, t) {
		return domain.Group{}, domain.NotFound()
	}
	return g, nil
}
func (s Service) User(st *domain.State, id string) (domain.User, error) {
	u, ok := st.Users[id]
	if !ok {
		return domain.User{}, domain.NotFound()
	}
	return u, nil
}
func (s Service) ActiveKey(st *domain.State, id string) (domain.APIKey, error) {
	k, ok := st.Keys[id]
	if !ok {
		return domain.APIKey{}, domain.NotFound()
	}
	if !k.Active() {
		return domain.APIKey{}, domain.Problem(409, "KEY_INACTIVE", "Key inactive")
	}
	return k, nil
}
