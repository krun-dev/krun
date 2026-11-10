package auth

import (
	"context"
	"errors"
	"time"

	"github.com/krun-dev/krun/internal/audit"
	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/krun-dev/krun/internal/storage"
)

type Service struct {
	Store         *storage.Store
	SourceEnabled func(string) bool
}
type LoginInput struct {
	Provider                                               appconfig.ProviderConfig
	Identity                                               domain.ExternalIdentity
	BrowserHash                                            string
	ExpectedGeneration                                     int
	SessionToken, OldSessionHash, ChallengeHash, RequestID string
}

func (s *Service) CommitLogin(ctx context.Context, in LoginInput) error {
	id := in.Identity

	ctx = context.WithValue(ctx, storage.LoginScopeKey{}, storage.LoginScope{Source: in.Provider.Source, Subject: id.Subject})

	at := time.Now().UTC()
	session := domain.Session{Hash: securetoken.Digest(in.SessionToken), CSRF: securetoken.Random(), Created: at, Seen: at, Absolute: at.Add(8 * time.Hour)}
	for attempt := 0; ; attempt++ {
		err := s.Store.Update(ctx, storage.PartitionIAM, func(st *domain.State) error {

			var user domain.User
			for _, u := range st.Users {
				if u.Source == in.Provider.Source && u.Subject == id.Subject {
					user = u
					break
				}
			}
			if user.ID == "" {
				invite := in.Provider.Source + "\x00" + id.Subject
				if in.Provider.Registration != "jit" && !at.Before(st.Invitations[invite]) {
					return domain.Problem(403, "REGISTRATION_DENIED", "Invitation required")
				}
				delete(st.Invitations, invite)
				user = domain.User{ID: securetoken.NewID("usr"), Source: in.Provider.Source, Subject: id.Subject, Status: "active", AuthVersion: 1}
			}
			if user.Status != "active" {
				return domain.Problem(403, "USER_DISABLED", "User disabled")
			}
			user.Name = id.Name
			user.Email = id.Email
			user.EmailVerified = id.EmailVerified
			st.Users[user.ID] = user
			st.Revision++
			session.User = user.ID
			session.AuthVersion = user.AuthVersion
			oldSessionHash := in.OldSessionHash
			st.PendingLogin = &domain.LoginCommit{Session: session, DeleteSessionHash: oldSessionHash, BrowserHash: in.BrowserHash, ExpectedGeneration: in.ExpectedGeneration, EmailChallengeHash: in.ChallengeHash}
			audit.Record(st, authorization.Principal{User: user, Kind: "session"}, in.RequestID, "auth.login", user.ID, true)
			return nil
		})
		if err == nil {
			break
		}
		var conflict *domain.APIError
		if attempt == 0 && errors.As(err, &conflict) && conflict.Code == "REVISION_CONFLICT" {

			continue
		}
		return err
	}
	return nil

}

type LoginChallenge struct{ State, Verifier, Nonce string }

func (s *Service) BeginLogin(ctx context.Context, provider appconfig.ProviderConfig, browserHash string, browser domain.Browser, returnTo string) (LoginChallenge, error) {
	out := LoginChallenge{State: securetoken.Random(), Verifier: securetoken.Random(), Nonce: securetoken.Random()}
	login := domain.Login{Provider: provider.ID, Browser: browserHash, Fingerprint: appconfig.LoginFingerprint(provider), Verifier: out.Verifier, Nonce: out.Nonce, ReturnTo: returnTo, Expires: time.Now().UTC().Add(5 * time.Minute)}
	_, err := s.Store.BeginLoginState(ctx, browserHash, browser, securetoken.Digest(out.State), login)
	return out, err
}
func (s *Service) ConsumeLogin(ctx context.Context, state, browserHash string, provider appconfig.ProviderConfig) (domain.Login, error) {
	invalid := func() (domain.Login, error) {
		return domain.Login{}, domain.Problem(403, "AUTH_STATE_INVALID", "Login state changed")
	}
	stateHash := securetoken.Digest(state)
	login, ok, err := s.Store.LoginState(ctx, stateHash)
	if err != nil {
		return invalid()
	}
	browser, hasBrowser, err := s.Store.BrowserFor(ctx, login.Browser)
	if err != nil {
		return invalid()
	}
	if !ok || !hasBrowser || login.Consumed || login.Provider != provider.ID || login.Browser != browserHash || login.Generation != browser.Generation || !time.Now().UTC().Before(login.Expires) || login.Fingerprint != appconfig.LoginFingerprint(provider) {
		return invalid()
	}
	consumed, err := s.Store.ConsumeLoginState(ctx, stateHash)
	if err != nil || !consumed {
		return invalid()
	}
	return login, nil
}
func (s *Service) LoginCurrent(ctx context.Context, tx domain.Login) bool {
	browser, ok, err := s.Store.BrowserFor(ctx, tx.Browser)
	return err == nil && ok && browser.Generation == tx.Generation && time.Now().UTC().Before(tx.Expires)
}
