package repository

import (
	"context"
	"time"

	repooauth "github.com/krun-dev/krun/internal/adapters/repositoryoauth"
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/krun-dev/krun/internal/storage"
)

func AuditEntry(p authorization.Principal, requestID, action, provider, organization string, allowed bool) domain.Audit {
	keyID := ""
	if p.Key != nil {
		keyID = p.Key.ID
	}
	return domain.Audit{ID: securetoken.NewID("audit"), Time: time.Now().UTC(), Actor: p.User.ID, Credential: p.Kind, KeyID: keyID, Action: action, Resource: provider, Organization: organization, Allowed: allowed, RequestID: requestID}
}
func (s *Service) RecordAudit(ctx context.Context, pending domain.WorkerRepositoryConnectState, requestID, action string, allowed bool) {
	_ = s.Store.AppendAudits(ctx, []domain.Audit{AuditEntry(authorization.Principal{User: domain.User{ID: pending.User}, Kind: "session"}, requestID, action, pending.Provider, pending.Organization, allowed)})
}
func (s *Service) StartConnection(ctx context.Context, st *domain.State, p authorization.Principal, organization, providerID, browser, requestID string) (string, error) {
	provider, err := s.RepositoryProvider(providerID, organization)
	if err != nil {
		return "", err
	}
	conf := (&repooauth.Client{Config: s.Config}).RepositoryOAuthConfig(provider)
	if conf == nil {
		return "", domain.Problem(409, "WORKER_REPOSITORY_OAUTH_DISABLED", "This provider is not configured for user connections")
	}
	state := securetoken.Random()
	pending := domain.WorkerRepositoryConnectState{User: p.User.ID, Provider: provider.ID, Organization: organization, Browser: browser, Expires: time.Now().UTC().Add(10 * time.Minute)}
	st.RecordAudit(AuditEntry(p, requestID, "workers.repository.connect.start", provider.ID, organization, true))
	if s.Store.Persistent() {
		audits := append([]domain.Audit(nil), st.PendingAudits...)
		st.PendingAudits = nil
		err = s.Store.UpsertRepositoryConnectStateAudited(ctx, state, pending, audits)
	} else {
		err = s.Store.UpsertRepositoryConnectState(ctx, state, pending)
	}
	if err != nil {
		return "", err
	}
	return conf.AuthCodeURL(state), nil
}

type ConnectInput struct {
	State, Code, Provider, Browser, RequestID string
	Denied                                    bool
}
type ConnectResult struct{ Provider, Organization string }

func (s *Service) CompleteConnection(ctx context.Context, in ConnectInput) (ConnectResult, error) {
	var pending domain.WorkerRepositoryConnectState
	var consumed bool
	var err error
	if s.Store.Persistent() {
		pending, consumed, err = s.Store.ConsumeRepositoryConnectState(ctx, in.State, in.Browser)
	} else {
		err = s.Store.Update(ctx, storage.PartitionWorkers, func(*domain.State) error {
			pending, consumed, err = s.Store.ConsumeRepositoryConnectState(ctx, in.State, in.Browser)
			return err
		})
	}
	if err != nil {
		return ConnectResult{}, err
	}
	if !consumed {
		return ConnectResult{}, domain.Problem(403, "WORKER_REPOSITORY_STATE_INVALID", "Connect request expired; try again")
	}
	out := ConnectResult{Provider: pending.Provider, Organization: pending.Organization}
	failed := func(err error) (ConnectResult, error) {
		s.RecordAudit(ctx, pending, in.RequestID, "workers.repository.connect.failed", false)
		return out, err
	}
	if in.Denied {
		return failed(domain.Problem(400, "WORKER_REPOSITORY_OAUTH_DENIED", "Provider rejected the connection"))
	}
	if pending.Provider != in.Provider {
		return failed(domain.Problem(403, "WORKER_REPOSITORY_STATE_INVALID", "Connect request expired; try again"))
	}
	provider, err := s.RepositoryProvider(pending.Provider, pending.Organization)
	if err != nil {
		return failed(err)
	}
	if provider.ClientID == "" {
		return failed(domain.Problem(409, "WORKER_REPOSITORY_OAUTH_DISABLED", "This provider is not configured for user connections"))
	}
	// The exchange happens before the bounded metadata write, never in a database transaction.
	token, err := (&repooauth.Client{Config: s.Config}).Exchange(ctx, provider, in.Code)
	if err != nil {
		if repooauth.IsUnavailable(err) {
			return failed(domain.Problem(503, "WORKER_REPOSITORY_OAUTH_UNAVAILABLE", "Repository authorization is unavailable"))
		}
		return failed(domain.Problem(400, "WORKER_REPOSITORY_OAUTH_DENIED", "Provider rejected the connection"))
	}
	connection := domain.WorkerRepositoryConnection{User: pending.User, Provider: authorizationKey(provider), AccessToken: token.AccessToken, TokenType: token.TokenType, RefreshToken: token.RefreshToken, ExpiresAt: token.Expiry, ConnectedAt: time.Now().UTC(), RefreshState: "ready"}
	entry := AuditEntry(authorization.Principal{User: domain.User{ID: pending.User}, Kind: "session"}, in.RequestID, "workers.repository.connect", pending.Provider, pending.Organization, true)
	if s.Store.Persistent() {
		err = s.Store.UpsertRepositoryConnectionAudited(ctx, connection, []domain.Audit{entry})
	} else {
		err = s.Store.Update(ctx, storage.PartitionWorkers, func(st *domain.State) error {
			if err := s.Store.UpsertRepositoryConnection(ctx, connection); err != nil {
				return err
			}
			st.RecordAudit(entry)
			return nil
		})
	}
	return out, err
}
func (s *Service) Disconnect(ctx context.Context, st *domain.State, p authorization.Principal, organization, provider, requestID string) error {
	configured, err := s.RepositoryProvider(provider, organization)
	if err != nil {
		return err
	}
	st.RecordAudit(AuditEntry(p, requestID, "workers.repository.disconnect", provider, organization, true))
	if s.Store.Persistent() {
		audits := append([]domain.Audit(nil), st.PendingAudits...)
		st.PendingAudits = nil
		return s.Store.DeleteRepositoryConnectionAudited(ctx, p.User.ID, authorizationKey(configured), audits)
	}
	return s.Store.DeleteRepositoryConnection(ctx, p.User.ID, authorizationKey(configured))
}
