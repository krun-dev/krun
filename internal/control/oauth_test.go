package control

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	authapp "github.com/krun-dev/krun/internal/auth"
)

const oauthTestVerifier = "oauth-pkce-fixture-verifier-012345678901234567890123456789"

func oauthBrowser(t *testing.T, s *Server, store *Store) {
	t.Helper()
	at := now()
	session := Session{User: "admin", AuthVersion: 1, CSRF: "oauth-csrf", Created: at, Seen: at, Absolute: at.Add(time.Hour)}
	if err := store.InsertSession(t.Context(), "oauth-browser", session); err != nil {
		t.Fatal(err)
	}
}
func oauthFormTest(s *Server, path string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func oauthApproval(s *Server, id string, allow bool) *httptest.ResponseRecorder {
	value := "false"
	if allow {
		value = "true"
	}
	r := httptest.NewRequest("POST", oauthPrefix+"/requests/"+id, strings.NewReader(`{"allow":`+value+`}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.Config.PublicURL)
	r.Header.Set("X-CSRF-Token", "oauth-csrf")
	r.AddCookie(&http.Cookie{Name: s.cookieName("session"), Value: "oauth-browser"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func oauthStartTest(t *testing.T, s *Server, client, scope string) (string, url.Values) {
	t.Helper()
	sum := sha256.Sum256([]byte(oauthTestVerifier))
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {"http://127.0.0.1:8976/oauth/callback"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "state": {"fixture-state-0123456789"}, "scope": {scope}}
	w := request(s, "GET", oauthPrefix+"/authorize?"+q.Encode(), "", "")
	if w.Code != 303 {
		t.Fatalf("authorize %d %s", w.Code, w.Body.String())
	}
	target, _ := url.Parse(w.Header().Get("Location"))
	return target.Query().Get("oauth"), q
}
func oauthCodeTest(t *testing.T, s *Server, client, scope string) url.Values {
	t.Helper()
	id, q := oauthStartTest(t, s, client, scope)
	approved := oauthApproval(s, id, true)
	if approved.Code != 200 {
		t.Fatalf("approve %d %s", approved.Code, approved.Body.String())
	}
	var out struct {
		URL string `json:"redirect_url"`
	}
	_ = json.Unmarshal(approved.Body.Bytes(), &out)
	u, _ := url.Parse(out.URL)
	if u.Query().Get("state") != q.Get("state") {
		t.Fatal("state lost")
	}
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {u.Query().Get("code")}, "redirect_uri": {q.Get("redirect_uri")}, "code_verifier": {oauthTestVerifier}}
}
func oauthDecodeTest(t *testing.T, w *httptest.ResponseRecorder) authapp.OAuthTokens {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("token %d %s", w.Code, w.Body.String())
	}
	var out authapp.OAuthTokens
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Access == "" || out.Refresh == "" {
		t.Fatal("missing access/refresh")
	}
	return out
}
func TestOAuthPKCEOneShotRotationAndRevocation(t *testing.T) {
	s, store := testServer(t)
	oauthBrowser(t, s, store)
	f := oauthCodeTest(t, s, authapp.OAuthCLIClient, "krun:control offline_access")
	bad := url.Values{}
	for k, v := range f {
		bad[k] = append([]string(nil), v...)
	}
	bad.Set("code_verifier", strings.Repeat("x", 43))
	if w := oauthFormTest(s, oauthPrefix+"/token", bad); w.Code != 400 {
		t.Fatal("invalid PKCE accepted")
	}
	first := oauthDecodeTest(t, oauthFormTest(s, oauthPrefix+"/token", f))
	if w := oauthFormTest(s, oauthPrefix+"/token", f); w.Code != 400 {
		t.Fatal("code replay accepted")
	}
	if w := request(s, "GET", "/api/auth/me", first.Access, ""); w.Code != 200 {
		t.Fatalf("bearer %d %s", w.Code, w.Body.String())
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {authapp.OAuthCLIClient}, "refresh_token": {first.Refresh}}
	second := oauthDecodeTest(t, oauthFormTest(s, oauthPrefix+"/token", refresh))
	if first.Refresh == second.Refresh || first.Access == second.Access {
		t.Fatal("credentials did not rotate")
	}
	if w := oauthFormTest(s, oauthPrefix+"/token", refresh); w.Code != 400 {
		t.Fatal("refresh replay accepted")
	}
	if w := request(s, "GET", "/api/auth/me", second.Access, ""); w.Code != 401 {
		t.Fatal("refresh replay did not revoke the grant")
	}
	fresh := oauthDecodeTest(t, oauthFormTest(s, oauthPrefix+"/token", oauthCodeTest(t, s, authapp.OAuthCLIClient, "krun:control offline_access")))
	if w := oauthFormTest(s, oauthPrefix+"/revoke", url.Values{"client_id": {authapp.OAuthCLIClient}, "token": {fresh.Refresh}}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/auth/me", fresh.Access, ""); w.Code != 401 {
		t.Fatal("revoke left access valid")
	}
}
func TestOAuthAccessLogoutRevokesItsGrant(t *testing.T) {
	s, store := testServer(t)
	oauthBrowser(t, s, store)

	issue := func() authapp.OAuthTokens {
		form := oauthCodeTest(t, s, authapp.OAuthCLIClient, "krun:control offline_access")
		return oauthDecodeTest(t, oauthFormTest(s, oauthPrefix+"/token", form))
	}
	refreshForm := func(token string) url.Values {
		return url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {authapp.OAuthCLIClient},
			"refresh_token": {token},
		}
	}
	first := issue()
	second := oauthDecodeTest(t, oauthFormTest(s, oauthPrefix+"/token", refreshForm(first.Refresh)))
	other := issue()

	browserCookies := []*http.Cookie{{Name: s.cookieName("session"), Value: "oauth-browser"}}
	if w := browserRequest(s, http.MethodPost, "/api/auth/logout", url.Values{"csrf_token": {"oauth-csrf"}}, browserCookies); w.Code != http.StatusNoContent {
		t.Fatalf("browser logout returned %d", w.Code)
	}
	if w := request(s, http.MethodGet, "/api/auth/me", second.Access, ""); w.Code != http.StatusOK {
		t.Fatalf("browser logout affected OAuth access: %d", w.Code)
	}
	if w := request(s, http.MethodPost, "/api/auth/logout", first.Access, "{}"); w.Code != http.StatusNoContent {
		t.Fatalf("OAuth access logout returned %d", w.Code)
	}

	for _, token := range []string{first.Access, second.Access} {
		if w := request(s, http.MethodGet, "/api/auth/me", token, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("access from the revoked grant returned %d", w.Code)
		}
	}
	w := oauthFormTest(s, oauthPrefix+"/token", refreshForm(second.Refresh))
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || w.Code != http.StatusBadRequest || failure.Error != "invalid_grant" {
		t.Errorf("refresh after logout returned %d with error %q", w.Code, failure.Error)
	}
	if w := request(s, http.MethodGet, "/api/auth/me", other.Access, ""); w.Code != http.StatusOK {
		t.Errorf("access from another grant returned %d", w.Code)
	}
}

func TestOAuthDeviceDenyAndApprovalBoundaries(t *testing.T) {
	s, store := testServer(t)
	oauthBrowser(t, s, store)
	start := oauthFormTest(s, oauthPrefix+"/device/auth", url.Values{"client_id": {authapp.OAuthCLIClient}, "scope": {"krun:control offline_access"}})
	var device struct {
		Code string `json:"device_code"`
		User string `json:"user_code"`
	}
	_ = json.Unmarshal(start.Body.Bytes(), &device)
	poll := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "client_id": {authapp.OAuthCLIClient}, "device_code": {device.Code}}
	if w := oauthFormTest(s, oauthPrefix+"/token", poll); w.Code != 400 || !strings.Contains(w.Body.String(), "authorization_pending") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(s, "POST", oauthPrefix+"/requests/"+device.User, "krun_cli_admin", `{"allow":true}`); w.Code != 403 {
		t.Fatal("CLI approved another terminal")
	}
	if w := oauthApproval(s, device.User, false); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := oauthFormTest(s, oauthPrefix+"/token", poll); w.Code != 400 || !strings.Contains(w.Body.String(), "access_denied") {
		t.Fatal("denial did not reach device client")
	}
	if w := oauthApproval(s, device.User, true); w.Code != 400 {
		t.Fatal("approval replay accepted")
	}
}
func TestOAuthRejectsUntrustedRedirectAndScopeEscalation(t *testing.T) {
	s, store, _ := workerTestServer(t)
	oauthBrowser(t, s, store)
	id, q := oauthStartTest(t, s, authapp.OAuthCLIClient, "krun:control offline_access")
	q.Set("redirect_uri", "https://attacker.invalid/callback")
	w := request(s, "GET", oauthPrefix+"/authorize?"+q.Encode(), "", "")
	if w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatal("untrusted redirect accepted")
	}
	q.Set("redirect_uri", "http://127.0.0.1:8976/oauth/callback")
	q.Set("code_challenge_method", "plain")
	if w = request(s, "GET", oauthPrefix+"/authorize?"+q.Encode(), "", ""); w.Code != 400 {
		t.Fatal("PKCE downgrade accepted")
	}
	r := httptest.NewRequest("POST", oauthPrefix+"/requests/"+id, strings.NewReader(`{"allow":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName("session"), Value: "oauth-browser"})
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	token := oauthDecodeTest(t, oauthFormTest(s, oauthPrefix+"/token", oauthCodeTest(t, s, authapp.OAuthWranglerClient, "account:read user:read workers:write d1:write offline_access")))
	if strings.Contains(token.Scope, "d1:write") {
		t.Fatal("unsupported resource scope granted")
	}
	if w = request(s, "GET", cfPrefix+"/accounts", token.Access, ""); w.Code != 200 {
		t.Fatalf("CF read failed %d %s", w.Code, w.Body.String())
	}
	if w = request(s, "POST", "/api/v1/orgs", token.Access, `{"name":"scope escape"}`); w.Code != 403 {
		t.Fatal("Wrangler token escaped its audience")
	}
}

func TestOAuthBrowserApprovalIsRateLimited(t *testing.T) {
	s, store := testServer(t)
	oauthBrowser(t, s, store)
	for i := 1; i <= 11; i++ {
		w := oauthApproval(s, "unknown-request", true)
		expected := 400
		if i == 11 {
			expected = 429
		}
		if w.Code != expected {
			t.Fatalf("approval %d: %d %s", i, w.Code, w.Body.String())
		}
	}
}
