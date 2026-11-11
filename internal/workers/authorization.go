package workers

import (
	"context"

	authorization "github.com/krun-dev/krun/internal/authorization"
	domain "github.com/krun-dev/krun/internal/domain"
	storage "github.com/krun-dev/krun/internal/storage"
)

func (s *Service) WorkerTarget(st *domain.State, ref WorkerRef, env, action string, p authorization.Principal) (authorization.Target, error) {
	t, e := authorization.Resolve(st, "worker", ref.Worker, env)
	if e != nil || t.Organization != ref.Organization {
		return t, domain.NotFound()
	}
	return t, s.require(st, p, action, t)
}

// Collection APIs use the union of visible environments, rather than requiring global permission.
func (s *Service) WorkerCollectionTarget(st *domain.State, ref WorkerRef, action string, p authorization.Principal) (authorization.Target, error) {
	t, e := authorization.Resolve(st, "worker", ref.Worker, "global")
	if e != nil || t.Organization != ref.Organization {
		return t, domain.NotFound()
	}
	for _, env := range workerEnvironmentNames(st.Workers.Workers[t.ID].Environments) {
		target := t
		target.Environment = env
		d, e := authorization.Authorize(st, p, action, target)
		if e != nil {
			return t, e
		}
		if d.Allowed {
			return t, s.require(st, p, action, target)
		}
	}
	return t, s.require(st, p, action, t)
}

// Reauthorize durable operations with current user/Key state before each privileged transition.
func (s *Service) WorkerAuthorizeActor(st *domain.State, a domain.WorkerActor, worker, env, action string) error {
	user, ok := st.Users[a.User]
	if !ok || user.Status != "active" || user.AuthVersion != a.AuthVersion || !s.sourceEnabled(user.Source) {
		return domain.Forbidden()
	}
	p := authorization.Principal{User: user}
	if a.Key != "" {
		key, ok := st.Keys[a.Key]
		if !ok || !key.Active() || key.Owner != a.User {
			return domain.Forbidden()
		}
		p.Key = &key
	}
	target, e := authorization.Resolve(st, "worker", worker, env)
	if e != nil {
		return e
	}
	d, e := authorization.Authorize(st, p, action, target)
	if e != nil {
		return e
	}
	if !d.Allowed {
		return domain.Forbidden()
	}
	return nil
}

// authorizeActor re-checks a durable operation's actor against current IAM
// state. Authorization needs both partitions: the grants and user rows are IAM,
// but resolve() looks the worker up in krun_resources, which is Workers.
func (w *Controller) AuthorizeActor(ctx context.Context, actor domain.WorkerActor, worker, env, action string) (error, error) {
	var denied error
	ctx, err := w.Service.Store.ActorScope(ctx, actor, worker)
	if err != nil {
		if _, ok := err.(*domain.APIError); ok {
			return err, nil
		}
		return nil, err
	}
	err = w.Service.Store.View(ctx, storage.PartitionIAM, func(st *domain.State) error {
		denied = w.Service.WorkerAuthorizeActor(st, actor, worker, env, action)
		return nil
	})
	return denied, err
}

func WorkerActor(p authorization.Principal) domain.WorkerActor {
	a := domain.WorkerActor{User: p.User.ID, AuthVersion: p.User.AuthVersion}
	if p.Key != nil {
		a.Key = p.Key.ID
	}
	return a
}
