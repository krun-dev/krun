package control

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestGenericOAuthPKCEAndMappedUserInfo(t *testing.T) {
	var verifier string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			verifier = r.Form.Get("code_verifier")
			if r.Form.Get("code") != "code" || r.Form.Get("client_secret") != "secret" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"access_token":"upstream-token","token_type":"Bearer"}`)
		case "/user":
			if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer upstream-token" || r.URL.Query().Get("locale") != "zh-CN" {
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"users":[{"id":9007199254740993,"name":"Test"}]}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	pc := ProviderConfig{ID: "oauth", Type: "oauth2", UserInfoURL: upstream.URL + "/user", SubjectPath: "data.users.0.id", NamePath: "data.users.0.name", UserInfo: UserInfoConfig{Method: "POST", Body: map[string]any{"profile": true}, Query: map[string]string{"locale": "zh-CN"}, Success: &SuccessCondition{Path: "code", Equals: json.Number("0")}}}
	p := &provider{config: pc, client: upstream.Client(), oauth: &oauth2.Config{ClientID: "client", ClientSecret: "secret", RedirectURL: "http://127.0.0.1/callback", Endpoint: oauth2.Endpoint{AuthURL: upstream.URL + "/authorize", TokenURL: upstream.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}}
	redirect := p.begin("state", "verifier", "nonce")
	if !strings.Contains(redirect, "code_challenge_method=S256") || !strings.Contains(redirect, "state=state") {
		t.Fatal("missing PKCE/state")
	}
	identity, e := p.complete(context.Background(), "code", "verifier", "nonce")
	if e != nil || identity.Subject != "9007199254740993" || identity.Name != "Test" || verifier != "verifier" {
		t.Fatal(identity, verifier, e)
	}
	p.config.UserInfo.Success.Equals = json.Number("1")
	if _, e = p.complete(context.Background(), "code", "verifier", "nonce"); e == nil {
		t.Fatal("failed business response accepted")
	}
}

func TestOIDCVerifiesSignatureIssuerAudienceExpiryAndNonce(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "test", Algorithm: "RS256", Use: "sig"}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	issuer := ""
	audience := "client"
	nonce := "nonce"
	expires := time.Now().Add(time.Hour).Unix()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			payload, _ := json.Marshal(map[string]any{"iss": issuer, "sub": "stable-subject", "aud": audience, "exp": expires, "iat": time.Now().Unix(), "nonce": nonce, "name": "OIDC User"})
			signed, _ := signer.Sign(payload)
			token, _ := signed.CompactSerialize()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "id_token": token})
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	issuer = upstream.URL
	idp, e := oidc.NewProvider(oidc.ClientContext(context.Background(), upstream.Client()), issuer)
	if e != nil {
		t.Fatal(e)
	}
	p := &provider{config: ProviderConfig{ID: "oidc", Type: "oidc", ClientID: "client"}, client: upstream.Client(), oidc: idp, oauth: &oauth2.Config{ClientID: "client", ClientSecret: "secret", Endpoint: oauth2.Endpoint{AuthURL: issuer + "/authorize", TokenURL: issuer + "/token", AuthStyle: oauth2.AuthStyleInParams}}}
	identity, e := p.complete(context.Background(), "code", "verifier", "nonce")
	if e != nil || identity.Subject != "stable-subject" {
		t.Fatal(identity, e)
	}
	nonce = "wrong"
	if _, e = p.complete(context.Background(), "code", "verifier", "nonce"); e == nil {
		t.Fatal("bad nonce accepted")
	}
	nonce = "nonce"
	audience = "other-client"
	if _, e = p.complete(context.Background(), "code", "verifier", "nonce"); e == nil {
		t.Fatal("bad audience accepted")
	}
	audience = "client"
	expires = time.Now().Add(-time.Hour).Unix()
	if _, e = p.complete(context.Background(), "code", "verifier", "nonce"); e == nil {
		t.Fatal("expired ID token accepted")
	}
	expires = time.Now().Add(time.Hour).Unix()
	issuer = "https://wrong-issuer.example"
	if _, e = p.complete(context.Background(), "code", "verifier", "nonce"); e == nil {
		t.Fatal("bad issuer accepted")
	}
}

func TestProviderFailureDiagnosticsDoNotExposeCredentials(t *testing.T) {
	const secret = "private-token-secret-and-auth-code"
	for _, tc := range []struct {
		name, stage, reason string
		status              int
	}{
		{"invalid-client", "token", "invalid_client", 401},
		{"untrusted-error", "token", "token_response_rejected", 400},
		{"userinfo-status", "userinfo", "http_response_rejected", 404},
		{"userinfo-redirect", "userinfo", "redirect_rejected", 0},
		{"userinfo-subject", "userinfo", "stable_subject_missing_or_invalid", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/token" {
					if tc.stage == "token" {
						w.WriteHeader(tc.status)
						code := "invalid_client"
						if tc.name == "untrusted-error" {
							code = secret
						}
						_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": secret})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": secret, "token_type": "Bearer"})
					return
				}
				switch tc.name {
				case "userinfo-status":
					w.WriteHeader(404)
					fmt.Fprint(w, secret)
				case "userinfo-redirect":
					http.Redirect(w, r, "/other?access_token="+secret, http.StatusFound)
				default:
					fmt.Fprint(w, `{"email":"private@example.test"}`)
				}
			}))
			defer upstream.Close()
			client := upstream.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return errUpstreamRedirect }
			p := &provider{config: ProviderConfig{Type: "oauth2", UserInfoURL: upstream.URL + "/user", SubjectPath: "sub"}, client: client, oauth: &oauth2.Config{ClientID: "client", ClientSecret: secret, Endpoint: oauth2.Endpoint{TokenURL: upstream.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}}
			_, err := p.complete(context.Background(), secret, "verifier", "")
			var failure *providerFailure
			if !errors.As(err, &failure) || failure.Stage != tc.stage || failure.Reason != tc.reason || failure.Status != tc.status {
				t.Fatalf("unexpected safe diagnostic: %v", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "private@example.test") {
				t.Fatal("credential exposed in diagnostic")
			}
		})
	}
}
