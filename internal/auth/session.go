package auth

import (
	context "context"
	"time"

	audit "github.com/krun-dev/krun/internal/audit"
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	securetoken "github.com/krun-dev/krun/internal/securetoken"
)

type Credentials struct {
	IsKey, HasSession, Bearer bool
	KeyID                     string
	Session                   domain.Session
}

func (s *Service) Authenticate(st *domain.State, c Credentials) (authorization.Principal, error) {
	p := authorization.Principal{}
	if c.IsKey {
		if k, ok := st.Keys[c.KeyID]; ok && k.Active() {
			p.User = st.Users[k.Owner]
			p.Kind = "api_key"
			p.Key = &k
		}
	} else if c.HasSession {
		session := c.Session

		if session.CLI == (c.Bearer) && time.Now().UTC().Before(session.Absolute) && (session.OAuthGrant != "" || time.Now().UTC().Sub(session.Seen) < 30*time.Minute) {
			u, exists := st.Users[session.User]
			if exists && u.AuthVersion == session.AuthVersion {
				p.User = u
				p.Kind = "session"
				p.SessionHash = session.Hash
				if session.CLI {
					p.Kind = "cli_session"
					if session.OAuthGrant != "" {
						p.OAuthScope = session.OAuthScope
						p.OAuthActions = OAuthActions(session.OAuthScope)
					}
				}
			}
		}
	}
	if p.User.ID == "" || p.User.Status != "active" || !s.SourceEnabled(p.User.Source) {
		return authorization.Principal{}, domain.Problem(401, "UNAUTHENTICATED", "Authentication required")
	}
	return p, nil

}

type CSRFResult struct{ Token, BrowserToken string }

func (s *Service) CSRF(ctx context.Context, sessionHash, browserHash, ip string) (CSRFResult, error) {
	session, ok, err := s.Store.SessionByHash(ctx, sessionHash)
	if err != nil {
		return CSRFResult{}, err
	}
	if ok && time.Now().UTC().Before(session.Absolute) && time.Now().UTC().Sub(session.Seen) < 30*time.Minute {
		return CSRFResult{Token: session.CSRF}, nil
	}
	browser, ok, err := s.Store.BrowserFor(ctx, browserHash)
	if err != nil {
		return CSRFResult{}, err
	}
	out := CSRFResult{}
	if !ok || !time.Now().UTC().Before(browser.Expires) {
		exceeded, err := s.Store.RateLimitExceeded(ctx, "csrf", ip, time.Minute, 30)
		if err != nil {
			return out, err
		}
		if exceeded {
			return out, domain.Problem(429, "RATE_LIMITED", "Try again later")
		}
		out.BrowserToken = securetoken.Random()
		browserHash = securetoken.Digest(out.BrowserToken)
		browser = domain.Browser{CSRF: securetoken.Random(), Expires: time.Now().UTC().Add(15 * time.Minute)}
		if err := s.Store.UpsertBrowser(ctx, browserHash, browser); err != nil {
			return out, err
		}
	}
	out.Token = browser.CSRF
	return out, nil
}
func (s *Service) Logout(ctx context.Context, st *domain.State, p authorization.Principal, browserHash, requestID string) error {
	audit.Record(st, p, requestID, "auth.logout", p.User.ID, true)
	if s.Store.Persistent() {
		audits := append([]domain.Audit(nil), st.PendingAudits...)
		st.PendingAudits = nil
		return s.Store.LogoutSessionAudited(ctx, p.SessionHash, browserHash, audits)
	}
	if err := s.Store.DeleteSession(ctx, p.SessionHash); err != nil {
		return err
	}
	browser, ok, err := s.Store.BrowserFor(ctx, browserHash)
	if err != nil {
		return err
	}
	if ok {
		browser.Generation++
		return s.Store.UpsertBrowser(ctx, browserHash, browser)
	}
	return nil
}
