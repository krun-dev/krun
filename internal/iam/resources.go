package iam

import (
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

func (s Service) Resources(st *domain.State, p authorization.Principal, t authorization.Target, kind string) ([]domain.Resource, error) {
	out := []domain.Resource{}
	for _, res := range st.Resources {
		if res.Organization != t.ID || res.Kind != kind {
			continue
		}
		target, _ := authorization.Resolve(st, kind, res.ID, "global")
		d, e := authorization.Authorize(st, p, kind+".read", target)
		if e != nil {
			return nil, e
		}
		if d.Allowed {
			out = append(out, res)
		}
	}
	return out, nil
}

func (s Service) CreateResource(st *domain.State, t authorization.Target, kind string, in NameInput) (domain.Resource, error) {
	if !nameValid(in.Name) {
		return domain.Resource{}, domain.Problem(422, "INVALID_REQUEST", "Name required")
	}
	res := domain.Resource{
		ID:           securetoken.NewID(kind),
		Kind:         kind,
		Name:         in.Name,
		Organization: t.ID,
		Revision:     1,
		Created:      time.Now().UTC().Truncate(time.Microsecond),
	}
	st.Resources[res.ID] = res
	st.Revision++
	return res, nil
}

func (s Service) UpdateResource(st *domain.State, t authorization.Target, in RevisionNameInput) (domain.Resource, error) {
	res := st.Resources[t.ID]
	if in.Revision != res.Revision {
		return domain.Resource{}, domain.Problem(409, "REVISION_CONFLICT", "Resource changed")
	}
	if !nameValid(in.Name) {
		return domain.Resource{}, domain.Problem(422, "INVALID_REQUEST", "Name required")
	}
	res.Name = in.Name
	res.Revision++
	st.Resources[t.ID] = res
	st.Revision++
	return res, nil
}

func (s Service) DeleteResource(st *domain.State, t authorization.Target) error {
	delete(st.Resources, t.ID)
	deletedGroups := map[string]bool{}
	for id, group := range st.Groups {
		if group.ScopeKind == t.Kind && group.ScopeID == t.ID {
			deletedGroups[id] = true
			delete(st.Groups, id)
		}
	}
	for id, a := range st.Assignments {
		if (a.ScopeKind == t.Kind && a.ScopeID == t.ID) || (a.SubjectKind == "group" && deletedGroups[a.SubjectID]) {
			delete(st.Assignments, id)
		}
	}
	st.Revision++
	return nil
}

// EnsureWorkerResource gives protocol retries a stable IAM identity. IAM and
// Workers remain separate commits; retrying can finish a partially created Worker.
func (s Service) EnsureWorkerResource(st *domain.State, t authorization.Target, name, id string) (domain.Resource, error) {
	if !nameValid(name) {
		return domain.Resource{}, domain.Problem(422, "INVALID_REQUEST", "Name required")
	}
	if id == "" {
		return domain.Resource{}, domain.Problem(422, "INVALID_REQUEST", "Worker identity required")
	}
	if old, ok := st.Resources[id]; ok {
		if old.Organization != t.ID || old.Kind != "worker" {
			return domain.Resource{}, domain.NotFound()
		}
		return old, nil
	}
	resource := domain.Resource{
		ID:           id,
		Organization: t.ID,
		Kind:         "worker",
		Name:         name,
		Revision:     1,
		Created:      time.Now().UTC().Truncate(time.Microsecond),
	}
	st.Resources[id] = resource
	st.Revision++
	return resource, nil
}
