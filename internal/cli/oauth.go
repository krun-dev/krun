package cli

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/krun-dev/krun/internal/localize"
	"github.com/krun-dev/krun/internal/securetoken"
)

const cliOAuthPath = "/api/auth/oauth"
const cliOAuthClient = "krun-cli"

type oauthTokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Expires int    `json:"expires_in"`
	Type    string `json:"token_type"`
	Scope   string `json:"scope"`
}

func (c *Client) oauthForm(ctx context.Context, path string, values url.Values, out any) error {
	if err := validateServer(c.Profile.Server); err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Profile.Server, "/")+cliOAuthPath+path, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept-Language", "en")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("OAuth redirects refused") }
	response, err := copy.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return err
	}
	if response.StatusCode != 200 {
		var e struct {
			Code        string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Code == "" {
			return fmt.Errorf("OAuth returned HTTP %d", response.StatusCode)
		}
		return &responseError{Status: response.StatusCode, Code: e.Code, Message: e.Description}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
func (c *Client) acceptOAuth(t oauthTokens) error {
	if !strings.HasPrefix(t.Access, "krun_cli_") || t.Type != "Bearer" || t.Expires <= 0 || t.Expires > 3600 || !strings.HasPrefix(t.Refresh, "krun_refresh_") {
		return errors.New("invalid OAuth credential")
	}
	c.Profile.Token = t.Access
	c.Profile.RefreshToken = t.Refresh
	c.Profile.Expires = time.Now().Add(time.Duration(t.Expires) * time.Second)
	return c.save()
}
func (c *Client) refreshOAuth(ctx context.Context) error {
	var t oauthTokens
	err := c.oauthForm(ctx, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Profile.RefreshToken}, "client_id": {cliOAuthClient}}, &t)
	if err != nil {
		var invalid *responseError
		if errors.As(err, &invalid) && invalid.Code == "invalid_grant" {
			c.clearCredentials()
			_ = c.save()
		}
		return err
	}
	return c.acceptOAuth(t)
}
func (c *Client) clearCredentials() {
	c.Profile.Token = ""
	c.Profile.RefreshToken = ""
	c.Profile.Expires = time.Time{}
}
func (c *Client) request(ctx context.Context, method, path string, input, output any, anonymous bool, headers ...func(http.Header)) error {
	if !anonymous && c.Profile.RefreshToken != "" && !c.Profile.Expires.IsZero() && time.Until(c.Profile.Expires) < 30*time.Second {
		if err := c.refreshOAuth(ctx); err != nil {
			return err
		}
	}
	err := c.requestOnce(ctx, method, path, input, output, anonymous, headers...)
	var expired *responseError
	if !anonymous && errors.As(err, &expired) && expired.Status == 401 && c.Profile.RefreshToken != "" {
		if e := c.refreshOAuth(ctx); e != nil {
			return e
		}
		return c.requestOnce(ctx, method, path, input, output, anonymous, headers...)
	}
	return err
}
func openLoginBrowser(target string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "linux":
		command = exec.Command("xdg-open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	}
	if command != nil {
		_ = command.Run()
	}
}
func (c *Client) browserLogin(ctx context.Context, browser bool, port int, scope string) error {
	if err := validateServer(c.Profile.Server); err != nil {
		return err
	}
	if port < 0 || port > 65535 {
		return errors.New("callback port must be between 0 and 65535")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("cannot start login callback; use --callback-port 0 or --device: %w", err)
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/oauth/callback"
	state, verifier := securetoken.Random(), securetoken.Random()
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	query := url.Values{"response_type": {"code"}, "client_id": {cliOAuthClient}, "redirect_uri": {redirect}, "scope": {scope}, "state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	target := strings.TrimRight(c.Profile.Server, "/") + cliOAuthPath + "/authorize?" + query.Encode()
	type callback struct {
		code string
		err  error
	}
	received := make(chan callback, 1)
	var accepted atomic.Bool
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		if r.Method != "GET" || r.URL.Path != "/oauth/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid login state", 403)
			return
		}
		result := callback{code: q.Get("code")}
		if q.Get("error") != "" {
			result.err = errors.New("browser authorization denied")
		}
		if result.code == "" && result.err == nil {
			http.Error(w, "Missing authorization code", 400)
			return
		}
		if !accepted.CompareAndSwap(false, true) {
			http.Error(w, "Login already completed", 409)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, loginCallbackPage(q.Get("lang"), r.Header.Get("Accept-Language")))
		received <- result
	})}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(cleanup)
		_ = server.Close()
	}()
	go func() { _ = server.Serve(listener) }()
	fmt.Fprintf(c.Err, "Open %s\n", target)
	if browser {
		openLoginBrowser(target)
	}
	timeout, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var result callback
	select {
	case <-timeout.Done():
		return errors.New("browser login cancelled or expired")
	case result = <-received:
	}
	if result.err != nil {
		return result.err
	}
	var tokens oauthTokens
	if err = c.oauthForm(timeout, "/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {cliOAuthClient}, "code": {result.code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}, &tokens); err != nil {
		return err
	}
	if err = c.acceptOAuth(tokens); err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Logged in. Credentials refresh automatically; use krun logout to revoke access.")
	return nil
}
func (c *Client) deviceLogin(ctx context.Context, browser bool, scope string) error {
	var device struct {
		Code     string `json:"device_code"`
		UserCode string `json:"user_code"`
		URL      string `json:"verification_uri_complete"`
		Expires  int    `json:"expires_in"`
		Interval int    `json:"interval"`
	}
	if err := c.oauthForm(ctx, "/device/auth", url.Values{"client_id": {cliOAuthClient}, "scope": {scope}}, &device); err != nil {
		return err
	}
	base, _ := url.Parse(c.Profile.Server)
	target, err := url.Parse(device.URL)
	if err != nil || target.Scheme != base.Scheme || target.Host != base.Host {
		return errors.New("invalid verification URL")
	}
	fmt.Fprintf(c.Err, "Open %s\nConfirm code: %s\n", device.URL, device.UserCode)
	if browser {
		openLoginBrowser(device.URL)
	}
	expires := time.Duration(device.Expires) * time.Second
	if expires <= 0 || expires > 5*time.Minute {
		expires = 5 * time.Minute
	}
	timeout, cancel := context.WithTimeout(ctx, expires)
	defer cancel()
	interval := time.Duration(device.Interval) * time.Second
	if interval < 3*time.Second {
		interval = 3 * time.Second
	}
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-timeout.Done():
			return errors.New("browser login cancelled or expired")
		case <-timer.C:
		}
		var t oauthTokens
		err = c.oauthForm(timeout, "/token", url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "client_id": {cliOAuthClient}, "device_code": {device.Code}}, &t)
		if err != nil {
			var pending *responseError
			if errors.As(err, &pending) && pending.Code == "authorization_pending" {
				continue
			}
			return err
		}
		if err = c.acceptOAuth(t); err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "Logged in. Credentials refresh automatically; use krun logout to revoke access.")
		return nil
	}
}

const loginCallbackHTML = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>krun · Return to your terminal</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#111413;color:#f3f7f5;font:16px system-ui}main{margin:24px;max-width:440px;padding:48px;border:1px solid #303a35;border-radius:24px;background:#191e1b}b{color:#47db9e;letter-spacing:2px}h1{font-size:30px;letter-spacing:-1px}p{color:#a8b6ae;line-height:1.7}small{display:block;margin-top:32px;color:#789184}</style><main><b>krun.</b><h1>Return to your terminal</h1><p>Your browser has returned the authorization result. Your terminal will confirm when sign-in is complete.</p><small>You can close this tab.</small></main></html>`

func loginCallbackPage(preferred, acceptLanguage string) string {
	language := localize.RequestLanguage(acceptLanguage)
	if preferred == "en" || preferred == "zh-CN" {
		language = preferred
	}
	if language == localize.English {
		return loginCallbackHTML
	}
	return strings.NewReplacer(
		`lang="en"`, `lang="zh-CN"`,
		"Return to your terminal", "返回终端",
		"Your browser has returned the authorization result. Your terminal will confirm when sign-in is complete.", "浏览器已返回授权结果。登录完成后，终端会显示确认信息。",
		"You can close this tab.", "现在可以关闭此页面。",
	).Replace(loginCallbackHTML)
}
