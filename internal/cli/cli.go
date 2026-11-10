package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/configfile"
)

const defaultEndpoint = "http://127.0.0.1:8080"

type Profile struct {
	Endpoint     string    `json:"endpoint"`
	Organization string    `json:"org,omitempty"`
	Token        string    `json:"token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expires      time.Time `json:"expires_at,omitempty"`
}
type Client struct {
	Profile    Profile
	configPath string
	usedAPI    bool
	Path       string
	Out, Err   io.Writer
	In         io.Reader
	HTTP       *http.Client
}
type responseError struct {
	Status        int
	Code, Message string
	RequestID     string
}

func (e *responseError) Error() string {
	message := e.Code + ": " + e.Message
	if e.RequestID != "" {
		message += " (request ID: " + e.RequestID + ")"
	}
	return message
}
func loopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func validateEndpoint(endpoint string) error {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("endpoint must be an HTTPS origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopbackHost(u.Hostname())) {
		return errors.New("HTTPS required except loopback development")
	}
	return nil
}
func (c *Client) requestOnce(ctx context.Context, method, path string, input any, output any, anonymous bool, headers ...func(http.Header)) error {
	if e := validateEndpoint(c.Profile.Endpoint); e != nil {
		return e
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("relative API path required")
	}
	var raw []byte
	var e error
	if input != nil {
		raw, e = json.Marshal(input)
		if e != nil {
			return e
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Profile.Endpoint, "/")+path, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept-Language", "en")
	if !anonymous && c.Profile.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Profile.Token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("API redirects refused") }}
	}
	response, e := client.Do(r)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if e != nil {
		return e
	}
	if response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			return fmt.Errorf("API returned HTTP %d", response.StatusCode)
		}
		return &responseError{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message, RequestID: response.Header.Get("X-Request-ID")}
	}
	if len(headers) > 0 {
		headers[0](response.Header)
	}
	if output != nil && response.StatusCode != 204 {
		if err := json.Unmarshal(data, output); err != nil {
			return err
		}
	}
	c.usedAPI = true
	return nil
}
func (c *Client) save() error {
	dir := filepath.Dir(c.Path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	raw, e := configfile.Marshal(c.Path, c.Profile)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".krun-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, c.Path)
}
func (c *Client) call(ctx context.Context, method, path string, input any) error {
	var out json.RawMessage
	if e := c.request(ctx, method, path, input, &out, false); e != nil {
		return e
	}
	return c.outputJSON(out)
}
func defaultPath() string {
	root, e := os.UserConfigDir()
	if e != nil {
		root = "."
	}
	return filepath.Join(root, "krun", "config.yaml")
}
func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) (result error) {
	defer func() {
		if errors.Is(result, flag.ErrHelp) {
			result = nil
		}
	}()
	var profileSelection, configPath string
	var noUpdateCheck bool
	endpoint := os.Getenv("KRUN_ENDPOINT")
	filtered := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			filtered = append(filtered, args[i:]...)
			break
		}
		if args[i] == "--no-update-check" {
			noUpdateCheck = true
			continue
		}
		name, value, inline := strings.Cut(args[i], "=")
		if name == "--endpoint" || name == "--config" || name == "--profile" {
			if !inline {
				if i+1 >= len(args) {
					return fmt.Errorf("%s requires a value", name)
				}
				i++
				value = args[i]
			}
			if value == "" {
				return fmt.Errorf("%s requires a value", name)
			}
			switch name {
			case "--endpoint":
				endpoint = value
			case "--profile":
				profileSelection = value
			case "--config":
				configPath = value
			}
		} else {
			filtered = append(filtered, args[i])
		}
	}
	if len(filtered) > 0 && filtered[0] == "--version" {
		if len(filtered) == 1 {
			_, err := fmt.Fprintln(out, "krun", Version)
			return err
		}
		if len(filtered) == 2 && filtered[1] == "--verbose" {
			_, err := fmt.Fprintf(out, "krun %s\nCommit: %s\nPublished: %s\n", Version, buildCommit, publishedAt)
			return err
		}
		return errors.New("use --version or --version --verbose")
	}
	if len(filtered) == 0 || filtered[0] == "--help" {
		fmt.Fprint(out, help)
		return nil
	}
	command := filtered[0]
	rest := filtered[1:]
	if configPath != "" && command != "deploy" {
		return errors.New("--config selects a Wrangler file for deploy --dry-run; use --profile to select CLI credentials")
	}
	path, e := profilePath(profileSelection)
	if e != nil {
		return e
	}
	c := &Client{Path: path, configPath: configPath, In: in, Out: out, Err: errOut}
	if !noUpdateCheck && command != "logout" && os.Getenv("CI") == "" &&
		terminalStream(in) && terminalStream(out) && terminalStream(errOut) {
		defer func() {
			if result == nil && c.usedAPI {
				if cacheDir, err := os.UserCacheDir(); err == nil {
					c.notifyCLIUpdate(ctx, cacheDir, time.Now())
				}
			}
		}()
	}
	token := os.Getenv("KRUN_TOKEN")
	if command == "deploy" {
		return c.deploy(ctx, command, rest, func() error {
			_, _, err := c.loadSession(endpoint, token)
			return err
		})
	}
	profile, temporaryEndpoint, e := c.loadSession(endpoint, token)
	if e != nil {
		return e
	}
	switch command {
	case "config":
		return c.configure(rest, profile)
	case "upgrade":
		return c.upgrade(ctx, rest)
	case "login":
		return c.login(ctx, rest)
	case "whoami":
		if len(rest) != 0 {
			return errors.New("whoami does not accept arguments")
		}
		return c.call(ctx, "GET", "/api/auth/me", nil)
	case "logout":
		if len(rest) != 0 {
			return errors.New("logout does not accept arguments")
		}
		if temporaryEndpoint || token != "" {
			return errors.New("logout requires the saved profile; remove temporary endpoint and KRUN_TOKEN overrides")
		}
		if c.Profile.RefreshToken != "" {
			if e = c.oauthForm(ctx, "/revoke", url.Values{"client_id": {cliOAuthClient}, "token": {c.Profile.RefreshToken}}, nil); e != nil {
				return e
			}
		} else if c.Profile.Token != "" && !strings.HasPrefix(c.Profile.Token, "krun_key_") {
			if e = c.request(ctx, "POST", "/api/auth/logout", map[string]any{}, nil, false); e != nil {
				var apiErr *responseError
				if !errors.As(e, &apiErr) || apiErr.Status != 401 {
					return e
				}
			}
		}
		c.Profile.clearCredentials()
		if e = c.save(); e != nil {
			return e
		}
		fmt.Fprintln(out, "Logged out.")
		return nil
	case "workers":
		return c.workers(ctx, rest)
	case "r2":
		return c.r2(ctx, rest)
	case "rollback":
		return c.deploy(ctx, command, rest, nil)
	case "deployments":
		return c.deployments(ctx, rest)
	case "versions":
		return c.versions(ctx, rest)
	case "secret":
		return c.secret(ctx, rest)
	case "org":
		return c.organization(ctx, rest)
	case "members", "groups", "roles", "keys", "models":
		return c.manage(ctx, command, rest)
	case "build":
		return c.build(ctx, rest)
	case "check":
		f := flags("check", errOut)
		action := f.String("action", "", "Action")
		kind := f.String("kind", "", "Resource kind")
		id := f.String("id", "", "Resource ID")
		environment := f.String("env", "global", "Target environment")
		if e = f.Parse(rest); e != nil {
			return e
		}
		if *action == "" || *kind == "" || *id == "" {
			return errors.New("--action, --kind and --id required")
		}
		return c.call(ctx, "POST", "/api/v1/authz/check", map[string]string{"action": *action, "resource_kind": *kind, "resource_id": *id, "environment": *environment})
	case "audit":
		f := flags("audit", errOut)
		org := c.orgFlag(f)
		system := f.Bool("system", false, "Query system audit instead of organization audit")
		limit := f.Int("limit", 50, "Audit page size (1–500)")
		cursor := f.String("cursor", "", "Cursor returned by the previous audit page")
		if e = f.Parse(rest); e != nil {
			return e
		}
		if *limit < 1 || *limit > 500 || f.NArg() != 0 {
			return errors.New("audit requires --limit between 1 and 500 and no positional arguments")
		}
		if flagSet(f, "org") {
			if *system {
				return errors.New("--system and --org cannot be combined")
			}
			if *org == "" {
				return errors.New("--org requires an organization ID")
			}
		}
		base := "/api/v1/system"
		if !*system && *org != "" {
			base = "/api/v1/orgs/" + url.PathEscape(*org)
		}
		query := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *cursor != "" {
			query.Set("cursor", *cursor)
		}
		return c.call(ctx, "GET", base+"/audit?"+query.Encode(), nil)
	case "api":
		f := flags("api", errOut)
		method := f.String("method", "GET", "HTTP method")
		path := f.String("path", "", "Control API path")
		file := f.String("file", "", "YAML request file")
		if e = f.Parse(rest); e != nil {
			return e
		}
		if !strings.HasPrefix(*path, "/api/v1/") {
			return errors.New("--path must begin /api/v1/")
		}
		input, e := dataFile(*file)
		if e != nil {
			return e
		}
		return c.call(ctx, *method, *path, input)
	default:
		return fmt.Errorf("unknown command %q; use krun --help", command)
	}
}
func flags(name string, errOut io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(errOut)
	return f
}
func (c *Client) login(ctx context.Context, args []string) error {
	f := flags("login", c.Err)
	stdin := f.Bool("token-stdin", false, "Read API Key from stdin")
	browser := f.Bool("browser", true, "Open the sign-in page in a browser")
	device := f.Bool("device", false, "Use a device code instead of a loopback callback")
	port := f.Int("callback-port", 8976, "Loopback callback port; 0 chooses a free port")
	scope := f.String("scopes", "krun:control offline_access", "OAuth scopes")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("login does not accept positional arguments")
	}
	callbackConfigured := false
	f.Visit(func(flag *flag.Flag) {
		if flag.Name == "callback-port" {
			callbackConfigured = true
		}
	})
	if *device && (*stdin || callbackConfigured) {
		return errors.New("--device cannot be combined with --token-stdin or --callback-port")
	}

	if *stdin {
		raw, e := io.ReadAll(io.LimitReader(c.In, 4096))
		if e != nil {
			return e
		}
		token := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(token, "krun_key_") {
			return errors.New("krun API Key required")
		}
		c.Profile.clearCredentials()
		c.Profile.Token = token
		var me any
		if e = c.request(ctx, "GET", "/api/auth/me", nil, &me, false); e != nil {
			return e
		}
		if e = c.save(); e != nil {
			return e
		}
		fmt.Fprintln(c.Out, "API Key saved.")
		return nil
	}
	requested := *scope
	if !strings.Contains(" "+requested+" ", " offline_access ") {
		requested += " offline_access"
	}
	if *device {
		return c.deviceLogin(ctx, *browser, requested)
	}
	return c.browserLogin(ctx, *browser, *port, requested)
}

func (c *Client) loadSession(endpoint, token string) (Profile, bool, error) {
	profile, err := loadProfile(c.Path)
	if err != nil {
		return profile, false, err
	}
	c.Profile = profile
	if c.Profile.Endpoint == "" {
		c.Profile.Endpoint = defaultEndpoint
	}
	temporaryEndpoint := endpoint != "" && strings.TrimRight(endpoint, "/") != strings.TrimRight(c.Profile.Endpoint, "/")
	if temporaryEndpoint {
		c.Profile.clearCredentials()
		c.Profile.Endpoint = endpoint
		c.Profile.Organization = ""
	}
	if token != "" {
		c.Profile.clearCredentials()
		c.Profile.Token = token
	}
	return profile, temporaryEndpoint, nil
}

func loadProfile(path string) (Profile, error) {
	var profile Profile
	if err := configfile.ValidatePath(path); err != nil {
		return profile, err
	}
	raw, err := os.ReadFile(path)

	if os.IsNotExist(err) {
		return profile, nil
	}
	if err != nil {
		return profile, err
	}
	if err := configfile.Decode(path, raw, &profile); err != nil {
		return profile, errors.New("invalid CLI config")
	}
	return profile, nil
}
func dataFile(path string) (any, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value any
	err = configfile.Decode(path, raw, &value)
	return value, err
}
func (c *Client) manage(ctx context.Context, command string, args []string) error {
	if len(args) == 0 {
		return errors.New("subcommand required")
	}
	operation := args[0]
	f := flags(command, c.Err)
	org := c.orgFlag(f)
	system := f.Bool("system", false, "System scope")
	id := f.String("id", "", "Resource / key / assignment ID")
	name := f.String("name", "", "Name")
	shortName := f.String("short-name", "", "Organization short name")
	locator := f.String("locator", "", "Resource locator")
	user := f.String("user", "", "Local user ID")
	group := f.String("group", "", "Group ID")
	role := f.String("role", "", "Role ID")
	kind := f.String("scope-kind", "organization", "Role scope kind")
	scopeID := f.String("scope-id", "", "Role scope ID")
	subjectKind := f.String("subject-kind", "user", "Role subject kind")
	subject := f.String("subject", "", "Subject ID")
	envs := f.String("environments", "", "Comma-separated environments")
	file := f.String("file", "", "YAML input file")
	policy := f.String("policy", "", "Key policy YAML file")
	expires := f.String("expires", "24h", "Key lifetime, e.g. 24h, 0 or never")
	revision := f.Int("revision", 0, "Expected revision")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if command == "models" && strings.Contains(*id, "/") {
		if *system {
			return errors.New("invalid resource path")
		}
		if err := resolveResourcePath(f, command, org, id); err != nil {
			return err
		}
	}
	base := "/api/v1/orgs/" + url.PathEscape(*org)
	if *system {
		if flagSet(f, "org") {
			return errors.New("--system and --org cannot be combined")
		}
		base = "/api/v1/system"
	}
	if command != "org" && !(command == "keys" && (operation == "show" || operation == "revoke" || operation == "update")) && *org == "" && !*system {
		return errors.New("specify --org, set a default with krun config set org, or use --system for system scope")
	}
	method := "GET"
	path := ""
	var input any
	switch command {
	case "org":
		path = "/api/v1/orgs"
		switch operation {
		case "list":
		case "create":
			method = "POST"
			values := map[string]string{"name": *name}
			if *shortName != "" {
				values["short_name"] = *shortName
			}
			input = values
		default:
			return errors.New("use org list or create")
		}
	case "members":
		path = base + "/members"
		switch operation {
		case "list":
		case "add":
			method = "POST"
			input = map[string]string{"user_id": *user}
		case "remove":
			method = "DELETE"
			path += "/" + url.PathEscape(*user)
		default:
			return errors.New("use members list, add or remove")
		}
	case "groups":
		path = base + "/groups"
		switch operation {
		case "list":
		case "create":
			method = "POST"
			input = map[string]string{"name": *name}
		case "update":
			if err := requireID(*id, "id"); err != nil {
				return err
			}
			if *file == "" && (strings.TrimSpace(*name) == "" || !flagSet(f, "revision") || *revision < 0) {
				return errors.New("--name and a non-negative --revision are required")
			}
			if *file != "" && (flagSet(f, "name") || flagSet(f, "revision")) {
				return errors.New("choose either --file or --name and --revision")
			}
			method = "PATCH"
			path += "/" + url.PathEscape(*id)
			input = map[string]any{"name": *name, "expected_revision": *revision}
		case "delete":
			method = "DELETE"
			path += "/" + url.PathEscape(*id)
		case "add-member", "remove-member":
			method = "PUT"
			if operation == "remove-member" {
				method = "DELETE"
			}
			path += "/" + url.PathEscape(*group) + "/members/" + url.PathEscape(*user)
		default:
			return errors.New("unknown groups operation")
		}
	case "roles":
		path = base + "/role-assignments"
		switch operation {
		case "list":
		case "grant", "update":
			method = "POST"
			if operation == "update" {
				method = "PUT"
				path += "/" + url.PathEscape(*id)
			}
			if *system {
				*kind = "system"
				*scopeID = "system"
			} else if *scopeID == "" {
				*scopeID = *org
			}
			environments := []string{}
			if *envs != "" {
				environments = strings.Split(*envs, ",")
			}
			input = map[string]any{"subject_kind": *subjectKind, "subject_id": *subject, "role_id": *role, "scope_kind": *kind, "scope_id": *scopeID, "conditions": map[string]any{"environments": environments}}
			if operation == "update" {
				input.(map[string]any)["expected_revision"] = *revision
			}
		case "revoke":
			method = "DELETE"
			path += "/" + url.PathEscape(*id)
		default:
			return errors.New("unknown roles operation")
		}
	case "keys":
		switch operation {
		case "list":
			path = base + "/api-keys"
		case "create", "update":
			method = "POST"
			path = base + "/api-keys"
			p, e := dataFile(*policy)
			if e != nil {
				return e
			}
			if p == nil {
				return errors.New("--policy required")
			}
			var lifetime time.Duration
			if operation == "create" && *expires != "never" && *expires != "0" {
				lifetime, e = time.ParseDuration(*expires)
				if e != nil || lifetime < 60*time.Second || lifetime > 90*24*time.Hour {
					return errors.New("--expires must be never, 0 or a duration between 60s and 2160h")
				}
			}
			input = map[string]any{"name": *name, "expires_in": int(lifetime.Seconds()), "policy": p}
			if operation == "update" {
				method = "PATCH"
				path = "/api/v1/api-keys/" + url.PathEscape(*id)
				input = map[string]any{"name": *name, "policy": p, "expected_revision": *revision}
			}
		case "show", "revoke":
			path = "/api/v1/api-keys/" + url.PathEscape(*id)
			if operation == "revoke" {
				method = "DELETE"
			}
		default:
			return errors.New("unknown keys operation")
		}
	case "models":
		path = base + "/" + command
		switch operation {
		case "list":
		case "create":
			method = "POST"
			values := map[string]string{"name": *name}
			if *locator != "" {
				values["locator"] = *locator
			}
			input = values
		case "get", "update", "delete":
			path += "/" + url.PathEscape(*id)
			if operation == "update" {
				method = "PATCH"
				input = map[string]any{"name": *name, "expected_revision": *revision}
			}
			if operation == "delete" {
				method = "DELETE"
			}
		default:
			return errors.New("unknown resource operation")
		}
	}
	if *file != "" {
		var e error
		input, e = dataFile(*file)
		if e != nil {
			return e
		}
	}
	return c.call(ctx, method, path, input)
}

const help = `krun — control-plane CLI

Workers
  krun workers --help
  krun build --help
  krun deploy [--org ID] --worker ID --env NAME [--build ID]
  krun deploy --dry-run [--config FILE] [--outdir DIR]
  krun rollback VERSION_ID [--org ID] --worker ID --env NAME
  krun deployments --help
  krun versions --help
  krun secret --help
  krun r2 --help

Account and configuration
  krun --version [--verbose]
  krun upgrade [--check]
  krun config --help
  krun login [--browser=false] [--device | --token-stdin] [--callback-port PORT]
  krun whoami | logout

Organizations and access
  krun org --help
  krun org invitations --help
  krun members list | add | remove --org ID [--user ID]
  krun groups list | create | delete --org ID [--name NAME | --id ID]
  krun groups update --org ID --id ID --name NAME --revision N
  krun groups add-member | remove-member --org ID --group ID --user ID
  krun roles list | grant | update | revoke --org ID [options]
    grant: --subject ID --subject-kind user|group --role ROLE
           [--scope-kind worker|model --scope-id ID --environments production]
    update: same grant options + --id ID --revision N
    revoke: --id ID
    System groups/roles: replace --org ID with --system
  krun keys list --org ID
  krun keys create --org ID --name NAME --policy FILE [--expires 24h|never]
  krun keys show | revoke --id ID
  krun keys update --id ID --name NAME --policy FILE --revision N
  krun models list | create | get | update | delete --org ID [options]

Diagnostics
  krun check --action ACTION --kind KIND --id ID [--env global]
  krun audit [--org ID | --system] [--limit 50] [--cursor CURSOR]
  krun api --method METHOD --path /api/v1/... [--file FILE]

Global options: --endpoint URL, --profile NAME|PATH, --no-update-check
Local dry-run option: --config WRANGLER_FILE
Organization commands use the profile default unless --org ID is specified.
Environment: KRUN_ENDPOINT, KRUN_TOKEN (for automation)
Browser-approved CLI sessions permit IAM administration; API Keys remain restricted.
`
