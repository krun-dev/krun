package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krun-dev/krun/internal/configfile"
)

type UserInfoConfig struct {
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Query   map[string]string `json:"query"`
	Body    map[string]any    `json:"body"`
	Success *SuccessCondition `json:"success,omitempty"`
}
type SuccessCondition struct {
	Path   string `json:"path"`
	Equals any    `json:"equals"`
}
type ProviderConfig struct {
	Enabled         *bool             `json:"enabled,omitempty"`
	ID              string            `json:"id"`
	Source          string            `json:"source_id"`
	Type            string            `json:"type"`
	Name            string            `json:"name"`
	ClientID        string            `json:"client_id"`
	ClientSecret    string            `json:"client_secret"`
	BaseURL         string            `json:"base_url"`
	Issuer          string            `json:"issuer"`
	AuthorizeURL    string            `json:"authorize_url"`
	TokenURL        string            `json:"token_url"`
	UserInfoURL     string            `json:"userinfo_url"`
	Scopes          []string          `json:"scopes"`
	Registration    string            `json:"registration"`
	SubjectPath     string            `json:"subject_path"`
	NamePath        string            `json:"name_path"`
	EmailPath       string            `json:"email_path"`
	UserInfo        UserInfoConfig    `json:"userinfo"`
	AuthorizeParams map[string]string `json:"authorize_params"`
	TokenAuth       string            `json:"token_client_auth"`
}

func (p ProviderConfig) enabled() bool { return p.Enabled == nil || *p.Enabled }

type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	TLS      string `json:"tls"`
}

type Config struct {
	Workers        *WorkersConfig `json:"workers,omitempty"`
	SMTP           SMTPConfig     `json:"smtp"`
	Listen         string         `json:"listen"`
	InternalListen string         `json:"internal_listen"`
	PublicURL      string         `json:"public_url"`
	DB             string         `json:"db"`
	Development    bool           `json:"development"`
	StaticDir      string         `json:"static_dir"`
	// TrustedProxyCIDRs is the explicit set of reverse-proxy source networks
	// allowed to supply X-Forwarded-For. The header is ignored for every other
	// peer so clients cannot choose their own rate-limit identity.
	TrustedProxyCIDRs []string         `json:"trusted_proxy_cidrs"`
	Providers         []ProviderConfig `json:"providers"`
	// OrganizationSelfService lets any signed-in user create an organization.
	// Off by default: organization.create remains system-admin only.
	OrganizationSelfService bool `json:"organization_self_service"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	if err := configfile.ValidatePath(path); err != nil {
		return c, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = configfile.DecodeWithEnv(path, raw, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

var slug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (c *Config) Validate() error {
	if err := c.Workers.validate(); err != nil {
		return err
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen must be host:port")
	}
	if c.Workers != nil && strings.TrimSpace(c.InternalListen) == "" {
		return errors.New("internal_listen required when Workers are configured")
	}
	if c.InternalListen != "" {
		if _, _, err := net.SplitHostPort(c.InternalListen); err != nil {
			return errors.New("internal_listen must be host:port")
		}
		if c.InternalListen == c.Listen {
			return errors.New("internal_listen must differ from listen")
		}
	}
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("public_url must be an origin without a subpath")
	}
	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	if u.Scheme != "https" && !(c.Development && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return errors.New("HTTPS required; development HTTP must use loopback")
	}
	if strings.TrimSpace(c.DB) == "" {
		return errors.New("PostgreSQL db connection string required")
	}
	for _, cidr := range c.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid trusted_proxy_cidrs entry %q", cidr)
		}
	}
	if _, err := pgxpool.ParseConfig(c.DB); err != nil {
		return errors.New("invalid PostgreSQL db connection string")
	}

	seen := map[string]bool{}
	sources := map[string]bool{}
	for i := range c.Providers {
		p := &c.Providers[i]
		if !slug.MatchString(p.ID) || !slug.MatchString(p.Source) || seen[p.ID] || sources[p.Source] {
			return errors.New("unique provider IDs and source IDs required")
		}
		seen[p.ID] = true
		sources[p.Source] = true
		if p.Registration == "" {
			p.Registration = "jit"
		}
		if p.Registration != "jit" && p.Registration != "invite_only" {
			return errors.New("invalid registration policy")
		}
		if !contains([]string{"github", "gitlab", "oauth2", "oidc", "dev", "email"}, p.Type) {
			return fmt.Errorf("unsupported provider type %s", p.Type)
		}
		if !p.enabled() {
			continue
		}
		switch p.Type {
		case "github":
			p.AuthorizeURL = "https://github.com/login/oauth/authorize"
			p.TokenURL = "https://github.com/login/oauth/access_token"
			p.UserInfoURL = "https://api.github.com/user"
			p.SubjectPath = "id"
			p.NamePath = "name"
			p.EmailPath = "email"
			if len(p.Scopes) == 0 {
				p.Scopes = []string{"read:user"}
			}
		case "gitlab":
			p.BaseURL = strings.TrimSuffix(p.BaseURL, "/")
			p.AuthorizeURL = p.BaseURL + "/oauth/authorize"
			p.TokenURL = p.BaseURL + "/oauth/token"
			p.UserInfoURL = p.BaseURL + "/api/v4/user"
			p.SubjectPath = "id"
			p.NamePath = "name"
			p.EmailPath = "email"
			if len(p.Scopes) == 0 {
				p.Scopes = []string{"read_user"}
			}
		case "oauth2":
			if p.SubjectPath == "" {
				return errors.New("oauth2 subject_path required")
			}
		case "oidc":
			if len(p.Scopes) == 0 {
				p.Scopes = []string{"openid", "profile", "email"}
			}
			if !contains(p.Scopes, "openid") {
				return errors.New("OIDC openid scope required")
			}
		case "email":
			if err := c.validateSMTP(); err != nil {
				return err
			}
			continue
		case "dev":
			if !c.Development {
				return errors.New("dev provider forbidden outside development")
			}
			continue
		default:
			return fmt.Errorf("unsupported provider type %s", p.Type)
		}
		if p.UserInfo.Method == "" {
			p.UserInfo.Method = "GET"
		}
		if p.UserInfo.Method != "GET" && p.UserInfo.Method != "POST" {
			return errors.New("userinfo supports GET or POST")
		}
		if p.UserInfo.Method == "GET" && len(p.UserInfo.Body) > 0 {
			return errors.New("userinfo GET cannot carry body")
		}
		for name := range p.UserInfo.Headers {
			if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Host") {
				return errors.New("protected userinfo header override")
			}
		}
		for name := range p.AuthorizeParams {
			if contains([]string{"response_type", "state", "redirect_uri", "client_id", "scope", "nonce", "code_challenge", "code_challenge_method"}, name) {
				return errors.New("protected authorize parameter override")
			}
		}
		if p.TokenAuth == "" {
			if p.Type == "oidc" {
				p.TokenAuth = "client_secret_basic"
			} else {
				p.TokenAuth = "client_secret_post"
			}
		}
		if p.TokenAuth != "client_secret_post" && p.TokenAuth != "client_secret_basic" {
			return errors.New("invalid token client authentication")
		}
		if p.ClientID == "" || p.ClientSecret == "" {
			return errors.New("provider client_id and client_secret required")
		}
		endpoints := []string{p.AuthorizeURL, p.TokenURL, p.UserInfoURL}
		if p.Type == "oidc" {
			endpoints = []string{p.Issuer}
		}
		for _, endpoint := range endpoints {
			v, err := url.Parse(endpoint)
			if err != nil || v.Scheme != "https" || v.Host == "" || v.User != nil || v.Fragment != "" {
				return errors.New("provider endpoints must be fixed HTTPS URLs")
			}
		}
	}
	return nil
}
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func randomID(prefix string) string { return prefix + "_" + randomToken()[:16] }
func digest(value string) string    { b := sha256.Sum256([]byte(value)); return hex.EncodeToString(b[:]) }
func now() time.Time                { return time.Now().UTC() }
func namespace(p ProviderConfig) string {
	value := struct{ Type, Base, Issuer, Subject, Client, UserInfo string }{p.Type, p.BaseURL, p.Issuer, p.SubjectPath, p.ClientID, p.UserInfoURL}
	b, _ := json.Marshal(value)
	return digest(string(b))
}
func fingerprint(p ProviderConfig) string {
	return digest(p.ID + "\x00" + p.Source + "\x00" + namespace(p))
}

func loginFingerprint(p ProviderConfig) string { b, _ := json.Marshal(p); return digest(string(b)) }
