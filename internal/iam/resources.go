package iam

import (
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/resourcename"
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

func (s Service) CreateResource(st *domain.State, t authorization.Target, kind string, in ResourceCreateInput) (domain.Resource, error) {
	locator, err := resourcename.Make(firstName(in.Locator, in.Name), 1, resourcename.ResourceMax)
	if err != nil {
		return domain.Resource{}, err
	}
	return s.createResource(st, t, kind, in.Name, locator)
}

func (s Service) CreateWorkerScript(st *domain.State, t authorization.Target, name string) (domain.Resource, error) {
	// Native routes accept internal IDs as well. Keep that namespace
	// disjoint from user-supplied script names.
	if strings.HasPrefix(name, "worker_") && len(name) == len("worker_")+16 {
		return domain.Resource{}, domain.Problem(422, "LOCATOR_INVALID", "Choose a different Worker locator")
	}
	return s.createResource(st, t, "worker", name, name)
}

func (s Service) createResource(st *domain.State, t authorization.Target, kind, name, locator string) (domain.Resource, error) {
	if !nameValid(name) {
		return domain.Resource{}, domain.Problem(422, "INVALID_REQUEST", "Name required (max 128 bytes)")
	}
	if securetoken.IsID(locator, kind) {
		return domain.Resource{}, domain.Problem(422, "LOCATOR_INVALID", "Choose a different resource locator")
	}
	if kind == "worker" {
		switch locator {
		case "domains", "network-profiles", "repository-providers":
			return domain.Resource{}, domain.Problem(422, "LOCATOR_INVALID", "Choose a different Worker locator")
		}
	}
	for _, existing := range st.Resources {
		if existing.Organization == t.ID && existing.Kind == kind && existing.Locator == locator {
			return domain.Resource{}, domain.Problem(409, "LOCATOR_CONFLICT", "Resource locator already exists")
		}
	}
	res := domain.Resource{
		ID:           securetoken.NewID(kind),
		Kind:         kind,
		Name:         name,
		Locator:      locator,
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
