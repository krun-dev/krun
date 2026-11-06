package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/configfile"
	"github.com/krun-dev/krun/internal/domain"
	"golang.org/x/oauth2"
)

type Provider struct {
	Config appconfig.ProviderConfig
	Oauth  *oauth2.Config
	Oidc   *oidc.Provider
	Client *http.Client
}

type Identity = domain.ExternalIdentity

var ErrUpstreamRedirect = errors.New("upstream redirects disabled")

// Keep diagnostics independent of upstream bodies, URLs and credential values.
type ProviderFailure struct {
	Stage, Reason string
	Status        int
}

func (e *ProviderFailure) Error() string { return e.Stage + ": " + e.Reason }

func ProviderFailed(stage, reason string, status int) error {
	return &ProviderFailure{Stage: stage, Reason: reason, Status: status}
}

// Requests go only to configured hosts. Redirects are refused before forwarding credentials.
func NewHTTPClient(c appconfig.Config) *http.Client {
	allowed := map[string]bool{}
	for _, p := range c.Providers {
		for _, endpoint := range []string{p.AuthorizeURL, p.TokenURL, p.UserInfoURL, p.Issuer} {
			u, _ := url.Parse(endpoint)
			if u != nil {
				allowed[u.Hostname()] = true
			}
		}
	}
	transport := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10, Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil || !allowed[host] {
			return nil, errors.New("upstream host not allowed")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		for _, ip := range ips {
			if ip.IP.IsLoopback() || ip.IP.IsUnspecified() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() {
				return nil, errors.New("unsafe upstream address")
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("no upstream addresses")
		}
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUpstreamRedirect }}
}

func New(ctx context.Context, c appconfig.Config, p appconfig.ProviderConfig, client *http.Client) (*Provider, error) {
	out := &Provider{Config: p, Client: client}
	if p.Type == "dev" || p.Type == "email" {
		return out, nil
	}
	endpoint := oauth2.Endpoint{AuthURL: p.AuthorizeURL, TokenURL: p.TokenURL}
	if p.Type == "oidc" {
		ctx = oidc.ClientContext(ctx, client)
		idp, e := oidc.NewProvider(ctx, p.Issuer)
		if e != nil {
			return nil, fmt.Errorf("OIDC discovery failed for %s", p.ID)
		}
		endpoint = idp.Endpoint()
		for _, endpointURL := range []string{endpoint.AuthURL, endpoint.TokenURL} {
			u, e := url.Parse(endpointURL)
			issuer, _ := url.Parse(p.Issuer)
			if e != nil || u.Scheme != "https" || u.Host != issuer.Host {
				return nil, errors.New("OIDC endpoints must be HTTPS on configured issuer host")
			}
		}
		out.Oidc = idp

	}
	if p.TokenAuth == "client_secret_basic" {
		endpoint.AuthStyle = oauth2.AuthStyleInHeader
	} else {
		endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	out.Oauth = &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, Endpoint: endpoint, Scopes: p.Scopes, RedirectURL: c.PublicURL + "/api/auth/" + p.ID + "/callback"}
	return out, nil
}

func (p *Provider) Begin(state, verifier, nonce string) string {
	if p.Config.Type == "dev" {
		return "/api/auth/" + p.Config.ID + "/callback?state=" + url.QueryEscape(state) + "&code=dev"
	}
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	for name, value := range p.Config.AuthorizeParams {
		options = append(options, oauth2.SetAuthURLParam(name, value))
	}
	if p.Oidc != nil {
		options = append(options, oauth2.SetAuthURLParam("nonce", nonce))
	}
	return p.Oauth.AuthCodeURL(state, options...)
}

func (p *Provider) Complete(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	if p.Config.Type == "dev" {
		if code != "dev" {
			return Identity{}, errors.New("invalid dev code")
		}
		return Identity{Subject: "developer", Name: "本地开发用户"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.Client)
	token, e := p.Oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if e != nil {
		status := 0
		reason := "request_failed"
		var response *oauth2.RetrieveError
		if errors.As(e, &response) {
			if response.Response != nil {
				status = response.Response.StatusCode
			}
			switch response.ErrorCode {
			case "invalid_client", "invalid_grant", "invalid_scope", "invalid_request", "unauthorized_client", "unsupported_grant_type":
				reason = response.ErrorCode
			default:
				reason = "token_response_rejected"
			}
		}
		return Identity{}, ProviderFailed("token", reason, status)
	}
	if p.Oidc != nil {
		raw, ok := token.Extra("id_token").(string)
		if !ok || len(raw) > 64<<10 {
			return Identity{}, errors.New("ID token required")
		}
		verified, e := p.Oidc.Verifier(&oidc.Config{ClientID: p.Config.ClientID}).Verify(oidc.ClientContext(ctx, p.Client), raw)
		if e != nil || verified.Nonce != nonce || verified.Subject == "" || len(verified.Subject) > 512 {
			return Identity{}, errors.New("invalid ID token")
		}
		var claims struct {
			Name          string `json:"name"`
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
		}
		if e = verified.Claims(&claims); e != nil {
			return Identity{}, e
		}
		return Identity{Subject: verified.Subject, Name: claims.Name, Email: claims.Email, EmailVerified: claims.EmailVerified}, nil
	}
	endpointURL, e := url.Parse(p.Config.UserInfoURL)
	if e != nil {
		return Identity{}, e
	}
	query := endpointURL.Query()
	for name, value := range p.Config.UserInfo.Query {
		query.Set(name, value)
	}
	endpointURL.RawQuery = query.Encode()
	var requestBody io.Reader
	if len(p.Config.UserInfo.Body) > 0 {
		raw, e := json.Marshal(p.Config.UserInfo.Body)
		if e != nil {
			return Identity{}, e
		}
		requestBody = bytes.NewReader(raw)
	}
	method := p.Config.UserInfo.Method
	if method == "" {
		method = "GET"
	}
	req, e := http.NewRequestWithContext(ctx, method, endpointURL.String(), requestBody)
	if e != nil {
		return Identity{}, e
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "krun-control/0.1")
	for name, value := range p.Config.UserInfo.Headers {
		req.Header.Set(name, value)
	}
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, e := p.Client.Do(req)
	if e != nil {
		reason := "request_failed"
		if errors.Is(e, ErrUpstreamRedirect) {
			reason = "redirect_rejected"
		}
		var networkError net.Error
		if errors.As(e, &networkError) && networkError.Timeout() {
			reason = "request_timeout"
		}
		return Identity{}, ProviderFailed("userinfo", reason, 0)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Identity{}, ProviderFailed("userinfo", "http_response_rejected", response.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return Identity{}, errors.New("userinfo too large")
	}
	var data any
	if e = configfile.DecodeJSON(raw, &data); e != nil {
		return Identity{}, e
	}
	if success := p.Config.UserInfo.Success; success != nil {
		actual, e := Lookup(data, success.Path)
		if e != nil || !reflect.DeepEqual(actual, success.Equals) {
			return Identity{}, errors.New("userinfo business status failed")
		}
	}
	subject, e := Mapped(data, p.Config.SubjectPath, true)
	if e != nil || subject == "" || len(subject) > 512 {
		return Identity{}, ProviderFailed("userinfo", "stable_subject_missing_or_invalid", response.StatusCode)
	}
	name, _ := Mapped(data, p.Config.NamePath, false)
	email, _ := Mapped(data, p.Config.EmailPath, false)
	return Identity{Subject: subject, Name: name, Email: email}, nil
}

func Mapped(data any, path string, required bool) (string, error) {
	if path == "" {
		if required {
			return "", errors.New("required path")
		}
		return "", nil
	}
	value, e := Lookup(data, path)
	if e != nil {
		return "", e
	}

	switch v := value.(type) {
	case string:
		return v, nil
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return "", errors.New("integer ID required")
		}
		return string(v), nil
	case nil:
		if !required {
			return "", nil
		}
	}
	return "", errors.New("string or integer required")
}

func Lookup(data any, path string) (any, error) {
	value := data
	for _, segment := range strings.Split(path, ".") {
		switch object := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = object[segment]
			if !ok {
				return nil, errors.New("field missing")
			}
		case []any:
			index, e := strconv.Atoi(segment)
			if e != nil || index < 0 || index >= len(object) {
				return nil, errors.New("array index invalid")
			}
			value = object[index]
		default:
			return nil, errors.New("invalid field path")
		}
	}
	return value, nil
}
