package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/audit"
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/krun-dev/krun/internal/storage"
)

const OAuthCLIClient = "krun-cli"
const OAuthWranglerClient = "krun-wrangler"
const OAuthAccessLifetime = time.Hour
const OAuthGrantLifetime = 30 * 24 * time.Hour
const OAuthAuthorizationLifetime = 5 * time.Minute

type OAuthError struct{ Code, Description string }

func (e *OAuthError) Error() string             { return e.Code }
func oauthError(code, description string) error { return &OAuthError{code, description} }
func OAuthClientName(id string) string {
	switch id {
	case OAuthCLIClient:
		return "krun CLI"
	case OAuthWranglerClient:
		return "Wrangler"
	}
	return ""
}

var pkceChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var pkceVerifier = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func OAuthRedirectValid(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") && u.Port() != "" && u.User == nil && u.Path == "/oauth/callback" && u.RawQuery == "" && u.Fragment == ""
}

// Wrangler requests all its platform scopes by default. Issue only the supported
// subset, return it explicitly in the token response, and display it at consent.
func NormalizeOAuthScopes(client, raw string) (string, error) {
	if OAuthClientName(client) == "" {
		return "", oauthError("unauthorized_client", "Unknown OAuth client")
	}
	supported := []string{"account:read", "user:read", "workers:write", "workers_scripts:write", "offline_access"}
	unsupported := strings.Fields("k2.read k2.write workers_kv:write workers_routes:write workers_tail:read d1:write pages:write zone:read ssl_certs:write ai:write ai-search:write ai-search:run agent-memory:write queues:write pipelines:write secrets_store:write artifacts:write flagship:write containers:write cloudchamber:write connectivity:admin email_routing:write email_sending:write browser:write challenge-widgets.write")
	out := []string{}
	for _, scope := range strings.Fields(raw) {
		allowed := false
		if client == OAuthCLIClient {
			allowed = scope == "krun:control" || scope == "offline_access"
		} else {
			allowed = slices.Contains(supported, scope)
			if slices.Contains(unsupported, scope) {
				continue
			}
		}
		if !allowed {
			return "", oauthError("invalid_scope", "Unsupported OAuth scope")
		}
		if !slices.Contains(out, scope) {
			out = append(out, scope)
		}
	}
	if len(out) == 0 || len(out) == 1 && out[0] == "offline_access" {
		return "", oauthError("invalid_scope", "At least one supported scope is required")
	}
	sort.Strings(out)
	return strings.Join(out, " "), nil
}
func OAuthActions(scope string) []string {
	scopes := strings.Fields(scope)
	if slices.Contains(scopes, "krun:control") {
		return nil
	}
	out := []string{}
	if slices.Contains(scopes, "account:read") {
		out = append(out, "organization.read", "worker.list")
	}
	if slices.Contains(scopes, "workers:write") || slices.Contains(scopes, "workers_scripts:write") {
		out = append(out, "worker.list", "worker.create", "worker.read", "worker.update", "worker.delete", "worker.publish", "worker.rollback", "worker.version.read", "worker.version.create", "worker.invoke", "build.start", "source.read", "binding.attach", "binding.detach")
	}
	return out
}

type OAuthAuthorizeInput struct{ Client, Redirect, Challenge, ChallengeMethod, State, Scope, ResponseType string }

func (s *Service) StartOAuth(ctx context.Context, in OAuthAuthorizeInput, device bool) (string, domain.OAuthAuthorization, error) {
	scope, err := NormalizeOAuthScopes(in.Client, in.Scope)
	if err != nil {
		return "", domain.OAuthAuthorization{}, err
	}
	if !device && (!OAuthRedirectValid(in.Redirect) || in.ResponseType != "code" || in.ChallengeMethod != "S256" || !pkceChallenge.MatchString(in.Challenge) || len(in.State) < 16 || len(in.State) > 512) {
		return "", domain.OAuthAuthorization{}, oauthError("invalid_request", "A loopback callback, state and PKCE S256 challenge are required")
	}
	id := securetoken.Random()
	at := time.Now().UTC()
	a := domain.OAuthAuthorization{Hash: securetoken.Digest(id), Client: in.Client, Redirect: in.Redirect, Challenge: in.Challenge, State: in.State, Scope: scope, Decision: "pending", Created: at, Expires: at.Add(OAuthAuthorizationLifetime)}
	if device {
		raw := strings.ToUpper(securetoken.Digest(securetoken.Random())[:14])
		a.UserCode = raw[:7] + "-" + raw[7:]
	}
	if err = s.Store.InsertOAuthAuthorization(ctx, a); err != nil {
		return "", a, err
	}
	return id, a, nil
}
func (s *Service) OAuthRequest(ctx context.Context, id string) (domain.OAuthAuthorization, error) {
	kind, key := "hash", securetoken.Digest(id)
	if len(id) == 15 && id[7] == '-' {
		kind, key = "user", strings.ToUpper(id)
	}
	a, ok, err := s.Store.OAuthAuthorization(ctx, kind, key)
	if err != nil {
		return a, err
	}
	if !ok || !time.Now().Before(a.Expires) || a.Consumed {
		return a, oauthError("invalid_request", "Authorization request expired or already used")
	}
	return a, nil
}
func (s *Service) DecideOAuth(ctx context.Context, id string, p authorization.Principal, allow bool, requestID string, revision int) (string, error) {
	if p.Kind != "session" {
		return "", domain.Forbidden()
	}
	a, err := s.OAuthRequest(ctx, id)
	if err != nil {
		return "", err
	}
	if a.Decision != "pending" {
		return "", oauthError("invalid_request", "Authorization already decided")
	}
	code := securetoken.Random()
	a.Decision = "denied"
	a.User = p.User.ID
	a.AuthVersion = p.User.AuthVersion
	if allow {
		a.Decision = "approved"
		if a.Redirect != "" {
			a.CodeHash = securetoken.Digest(code)
		}
	}
	st := domain.NewState()
	st.Revision = revision
	audit.Record(st, p, requestID, "auth.oauth."+a.Decision, p.User.ID, allow)
	if err = s.Store.DecideOAuthAuthorization(ctx, a, st.PendingAudits); err != nil {
		if errors.Is(err, storage.ErrOAuthGrant) {
			return "", oauthError("invalid_request", "Authorization already decided or expired")
		}
		return "", err
	}
	if a.Redirect == "" {
		return "", nil
	}
	u, _ := url.Parse(a.Redirect)
	q := u.Query()
	q.Set("state", a.State)
	if allow {
		q.Set("code", code)
	} else {
		q.Set("error", "access_denied")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type OAuthTokenInput struct{ GrantType, Client, Code, Redirect, Verifier, Refresh, Device, Scope string }
type OAuthTokens struct {
	Access  string `json:"access_token"`
	Type    string `json:"token_type"`
	Expires int    `json:"expires_in"`
	Refresh string `json:"refresh_token,omitempty"`
	Scope   string `json:"scope"`
}

func (s *Service) ExchangeOAuth(ctx context.Context, in OAuthTokenInput) (OAuthTokens, error) {
	if OAuthClientName(in.Client) == "" {
		return OAuthTokens{}, oauthError("invalid_client", "Unknown OAuth client")
	}
	var a domain.OAuthAuthorization
	var g domain.OAuthGrant
	var r domain.OAuthRefresh
	var err error
	authHash, refreshHash := "", ""
	at := time.Now().UTC()
	switch in.GrantType {
	case "authorization_code", "urn:ietf:params:oauth:grant-type:device_code":
		key, kind := securetoken.Digest(in.Code), "code"
		if in.GrantType != "authorization_code" {
			key, kind = securetoken.Digest(in.Device), "hash"
		}
		var ok bool
		a, ok, err = s.Store.OAuthAuthorization(ctx, kind, key)
		if err != nil {
			return OAuthTokens{}, err
		}
		if !ok || a.Client != in.Client || a.Consumed {
			return OAuthTokens{}, oauthError("invalid_grant", "Invalid authorization grant")
		}
		if !at.Before(a.Expires) {
			return OAuthTokens{}, oauthError("expired_token", "Authorization request expired")
		}
		if in.GrantType == "authorization_code" {
			sum := sha256.Sum256([]byte(in.Verifier))
			challenge := base64.RawURLEncoding.EncodeToString(sum[:])
			if a.Redirect == "" || in.Redirect != a.Redirect || !pkceVerifier.MatchString(in.Verifier) || subtle.ConstantTimeCompare([]byte(challenge), []byte(a.Challenge)) != 1 {
				return OAuthTokens{}, oauthError("invalid_grant", "Invalid authorization grant")
			}
		} else if a.UserCode == "" {
			return OAuthTokens{}, oauthError("invalid_grant", "Invalid device grant")
		}
		if a.Decision == "pending" {
			return OAuthTokens{}, oauthError("authorization_pending", "Waiting for browser approval")
		}
		if a.Decision == "denied" {
			return OAuthTokens{}, oauthError("access_denied", "Authorization denied")
		}
		g = domain.OAuthGrant{ID: securetoken.NewID("grant"), User: a.User, Client: a.Client, Scope: a.Scope, AuthVersion: a.AuthVersion, Created: at, Expires: at.Add(OAuthGrantLifetime)}
		authHash = a.Hash
	case "refresh_token":
		var ok bool
		r, g, ok, err = s.Store.OAuthRefreshGrant(ctx, securetoken.Digest(in.Refresh))
		if err != nil {
			return OAuthTokens{}, err
		}
		if !ok || g.Client != in.Client || g.Revoked || !at.Before(r.Expires) || !at.Before(g.Expires) {
			return OAuthTokens{}, oauthError("invalid_grant", "Refresh token expired or revoked")
		}
		if r.Consumed {
			if err = s.Store.RevokeOAuthGrant(ctx, g.ID); err != nil {
				return OAuthTokens{}, err
			}
			return OAuthTokens{}, oauthError("invalid_grant", "Refresh token has already been used; sign in again")
		}
		if in.Scope != "" && in.Scope != g.Scope {
			return OAuthTokens{}, oauthError("invalid_scope", "Refresh scope must match the authorization")
		}
		refreshHash = r.Hash
	default:
		return OAuthTokens{}, oauthError("unsupported_grant_type", "Unsupported grant type")
	}
	user, ok, err := s.Store.OAuthUser(ctx, g.User)
	if err != nil {
		return OAuthTokens{}, err
	}
	if !ok || user.Status != "active" || user.AuthVersion != g.AuthVersion || !s.SourceEnabled(user.Source) {
		return OAuthTokens{}, oauthError("invalid_grant", "User access has changed; sign in again")
	}
	lifetime := min(OAuthAccessLifetime, g.Expires.Sub(at))
	if lifetime < time.Second {
		return OAuthTokens{}, oauthError("invalid_grant", "Authorization expired; sign in again")
	}
	out := OAuthTokens{Access: "krun_cli_" + securetoken.Random(), Type: "Bearer", Expires: int(lifetime.Seconds()), Scope: g.Scope}
	session := domain.Session{Hash: securetoken.Digest(out.Access), User: g.User, AuthVersion: g.AuthVersion, Created: at, Seen: at, Absolute: at.Add(time.Duration(out.Expires) * time.Second), CLI: true, OAuthGrant: g.ID, OAuthScope: g.Scope}
	next := domain.OAuthRefresh{}
	if slices.Contains(strings.Fields(g.Scope), "offline_access") {
		out.Refresh = "krun_refresh_" + securetoken.Random()
		next = domain.OAuthRefresh{Hash: securetoken.Digest(out.Refresh), Grant: g.ID, Created: at, Expires: g.Expires}
	}
	if err = s.Store.ExchangeOAuth(ctx, authHash, refreshHash, g, next, session); err != nil {
		if errors.Is(err, storage.ErrOAuthGrant) {
			if refreshHash != "" {
				if revokeErr := s.Store.RevokeOAuthGrant(ctx, g.ID); revokeErr != nil {
					return OAuthTokens{}, revokeErr
				}
			}
			return OAuthTokens{}, oauthError("invalid_grant", "Authorization grant has already been used or revoked")
		}
		return OAuthTokens{}, err
	}
	return out, nil
}
func (s *Service) RevokeOAuth(ctx context.Context, client, token string) error {
	if OAuthClientName(client) == "" {
		return oauthError("invalid_client", "Unknown OAuth client")
	}
	id, err := s.Store.OAuthGrantForToken(ctx, securetoken.Digest(token), client)
	if err != nil || id == "" {
		return err
	}
	return s.Store.RevokeOAuthGrant(ctx, id)
}

func (s *Service) CheckDeviceApproval(ctx context.Context, p authorization.Principal, ip string) error {
	if p.Kind != "session" {
		return domain.Forbidden()
	}
	for _, limit := range []struct {
		scope, key string
		max        int
	}{{"cli-approve-user", p.User.ID, 10}, {"cli-approve-ip", ip, 30}} {
		exceeded, err := s.Store.RateLimitExceeded(ctx, limit.scope, limit.key, time.Minute, limit.max)
		if err != nil {
			return err
		}
		if exceeded {
			return domain.Problem(429, "RATE_LIMITED", "Try again later")
		}
	}
	return nil
}
