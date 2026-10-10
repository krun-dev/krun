package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/configfile"
)

type Profile struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}
type Client struct {
	Profile  Profile
	Path     string
	Out, Err io.Writer
	In       io.Reader
	HTTP     *http.Client
}
type responseError struct {
	Status        int
	Code, Message string
}

func (e *responseError) Error() string { return e.Code + ": " + e.Message }
func validateServer(server string) error {
	u, e := url.Parse(server)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("server must be an HTTPS origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return errors.New("HTTPS required except loopback development")
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, path string, input any, output any, anonymous bool) error {
	if e := validateServer(c.Profile.Server); e != nil {
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
	r, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Profile.Server, "/")+path, bytes.NewReader(raw))
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
		return &responseError{response.StatusCode, envelope.Error.Code, envelope.Error.Message}
	}
	if output != nil && response.StatusCode != 204 {
		return json.Unmarshal(data, output)
	}
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
	var out any
	if e := c.request(ctx, method, path, input, &out, false); e != nil {
		return e
	}
	if out == nil {
		fmt.Fprintln(c.Out, "Done.")
		return nil
	}
	raw, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		return e
	}
	fmt.Fprintln(c.Out, string(raw))
	return nil
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
	path := defaultPath()
	server := os.Getenv("KRUN_SERVER")
	filtered := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--server" || args[i] == "--config" {
			if i+1 >= len(args) {
				return errors.New("missing global flag value")
			}
			value := args[i+1]
			if args[i] == "--server" {
				server = value
			} else {
				path = value
			}
			i++
		} else {
			filtered = append(filtered, args[i])
		}
	}
	c := &Client{Path: path, In: in, Out: out, Err: errOut}
	profile, e := loadProfile(path)
	if e != nil {
		return e
	}
	c.Profile = profile
	if server != "" && strings.TrimRight(server, "/") != strings.TrimRight(c.Profile.Server, "/") {
		c.Profile.Token = ""
		c.Profile.Server = server
	}
	if c.Profile.Server == "" {
		c.Profile.Server = "http://127.0.0.1:8080"
	}
	if token := os.Getenv("KRUN_TOKEN"); token != "" {
		c.Profile.Token = token
	}
	if len(filtered) == 0 || filtered[0] == "help" || filtered[0] == "--help" {
		fmt.Fprint(out, help)
		return nil
	}
	if filtered[0] == "auth" {
		filtered = filtered[1:]
		if len(filtered) == 0 {
			return errors.New("use auth login, logout or status")
		}
	}
	command := filtered[0]
	rest := filtered[1:]
	switch command {
	case "login":
		return c.login(ctx, rest)
	case "whoami", "status":
		return c.call(ctx, "GET", "/api/v1/me", nil)
	case "logout":
		if c.Profile.Token != "" && !strings.HasPrefix(c.Profile.Token, "krun_key_") {
			if e = c.request(ctx, "POST", "/auth/logout", map[string]any{}, nil, false); e != nil {
				var apiErr *responseError
				if !errors.As(e, &apiErr) || apiErr.Status != 401 {
					return e
				}
			}
		}
		c.Profile.Token = ""
		if e = c.save(); e != nil {
			return e
		}
		fmt.Fprintln(out, "Logged out.")
		return nil
	case "org", "orgs", "members", "groups", "roles", "keys", "services", "models":
		return c.manage(ctx, command, rest)
	case "check":
		f := flags("check", errOut)
		action := f.String("action", "", "Action")
		kind := f.String("kind", "", "Resource kind")
		id := f.String("id", "", "Resource ID")
		environment := f.String("environment", "global", "Target environment")
		if e = f.Parse(rest); e != nil {
			return e
		}
		if *action == "" || *kind == "" || *id == "" {
			return errors.New("--action, --kind and --id required")
		}
		return c.call(ctx, "POST", "/api/v1/authz/check", map[string]string{"action": *action, "resource_kind": *kind, "resource_id": *id, "environment": *environment})
	case "audit":
		f := flags("audit", errOut)
		org := f.String("org", "", "Organization (omit for system)")
		limit := f.Int("limit", 50, "Audit page size (1–500)")
		cursor := f.String("cursor", "", "Cursor returned by the previous audit page")
		if e = f.Parse(rest); e != nil {
			return e
		}
		if *limit < 1 || *limit > 500 || f.NArg() != 0 {
			return errors.New("audit requires --limit between 1 and 500 and no positional arguments")
		}
		base := "/api/v1/system"
		if *org != "" {
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
		return fmt.Errorf("unknown command %q; use krun help", command)
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
	noBrowser := f.Bool("no-browser", false, "Print URL without opening browser")
	if e := f.Parse(args); e != nil {
		return e
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
		c.Profile.Token = token
		var me any
		if e = c.request(ctx, "GET", "/api/v1/me", nil, &me, false); e != nil {
			return e
		}
		if e = c.save(); e != nil {
			return e
		}
		fmt.Fprintln(c.Out, "API Key saved.")
		return nil
	}
	var device struct {
		Code     string `json:"device_code"`
		UserCode string `json:"user_code"`
		URL      string `json:"verification_uri"`
		Expires  int    `json:"expires_in"`
		Interval int    `json:"interval"`
	}
	if e := c.request(ctx, "POST", "/auth/cli/start", map[string]any{}, &device, true); e != nil {
		return e
	}
	base, _ := url.Parse(c.Profile.Server)
	verify, e := url.Parse(device.URL)
	if e != nil || verify.Scheme != base.Scheme || verify.Host != base.Host {
		return errors.New("invalid verification URL")
	}
	fmt.Fprintf(c.Err, "Open %s\nConfirm code: %s\n", device.URL, device.UserCode)
	if !*noBrowser {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", device.URL)
		case "linux":
			cmd = exec.Command("xdg-open", device.URL)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", device.URL)
		}
		if cmd != nil {
			_ = cmd.Run()
		}
	}
	timeout := time.Duration(device.Expires) * time.Second
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	loginCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	interval := time.Duration(device.Interval) * time.Second
	if interval < 3*time.Second {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-loginCtx.Done():
			return errors.New("browser login cancelled or expired")
		case <-ticker.C:
			var result struct {
				Token string `json:"token"`
			}
			e = c.request(loginCtx, "POST", "/auth/cli/poll", map[string]string{"device_code": device.Code}, &result, true)
			if e != nil {
				var pending *responseError
				if errors.As(e, &pending) && pending.Code == "AUTHORIZATION_PENDING" {
					continue
				}
				return e
			}
			if !strings.HasPrefix(result.Token, "krun_cli_") {
				return errors.New("invalid CLI credential")
			}
			c.Profile.Token = result.Token
			if e = c.save(); e != nil {
				return e
			}
			fmt.Fprintln(c.Out, "Logged in. CLI session expires in one hour.")
			return nil
		}
	}
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
	org := f.String("org", "", "Organization ID")
	system := f.Bool("system", false, "System scope")
	id := f.String("id", "", "Resource / key / assignment ID")
	name := f.String("name", "", "Name")
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
	base := "/api/v1/orgs/" + url.PathEscape(*org)
	if *system {
		base = "/api/v1/system"
	}
	if command != "org" && command != "orgs" && !(command == "keys" && (operation == "show" || operation == "revoke" || operation == "update")) && *org == "" && !*system {
		return errors.New("--org or --system required")
	}
	method := "GET"
	path := ""
	var input any
	switch command {
	case "org", "orgs":
		path = "/api/v1/orgs"
		switch operation {
		case "list":
		case "create":
			method = "POST"
			input = map[string]string{"name": *name}
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
	case "services", "models":
		path = base + "/" + command
		switch operation {
		case "list":
		case "create":
			method = "POST"
			input = map[string]string{"name": *name}
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

  krun login [--server URL] [--no-browser | --token-stdin]
  krun whoami | logout
  krun org list | create --name NAME
  krun members list | add | remove --org ID [--user ID]
  krun groups list | create | delete --org ID [--name NAME | --id ID]
  krun groups add-member | remove-member --org ID --group ID --user ID
  krun roles list | grant | update | revoke --org ID [options]
    grant: --subject ID --subject-kind user|group --role ROLE
           [--scope-kind service|model --scope-id ID --environments staging]
    update: same grant options + --id ID --revision N
    revoke: --id ID
    System groups/roles: replace --org ID with --system
  krun keys list --org ID
  krun keys create --org ID --name NAME --policy FILE [--expires 24h|never]
  krun keys show | revoke --id ID
  krun keys update --id ID --name NAME --policy FILE --revision N
  krun services|models list | create | get | update | delete --org ID [options]
  krun check --action ACTION --kind KIND --id ID [--environment global]
  krun audit [--org ID] [--limit 50] [--cursor CURSOR]
  krun api --method METHOD --path /api/v1/... [--file FILE]

Global options: --server URL, --config FILE
Environment: KRUN_SERVER, KRUN_TOKEN (for automation)
Browser-approved CLI sessions permit IAM administration; API Keys remain restricted.
`
