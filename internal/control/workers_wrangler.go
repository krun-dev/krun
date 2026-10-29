package control

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/tailscale/hujson"
)

// wranglerConfig is the subset of a Cloudflare Worker configuration this
// platform understands. We deliberately mirror wrangler's own field names and
// value sets instead of inventing a parallel standard: a repository that builds
// with `wrangler deploy` should build here without editing its config.
type wranglerConfig struct {
	Main               string
	CompatibilityDate  string
	CompatibilityFlags []string
	Assets             *wranglerAssets
}

// wranglerAssets mirrors the `assets` block of wrangler.toml / wrangler.jsonc.
type wranglerAssets struct {
	Directory        string
	Binding          string
	RunWorkerFirst   json.RawMessage // boolean or []string, exactly as wrangler allows
	NotFoundHandling string
	HTMLHandling     string
}

// wranglerJSONAssets is the decode form: run_worker_first is a union type in
// wrangler, and encoding/json needs an interface to accept both shapes.
type wranglerJSONAssets struct {
	Directory        string      `json:"directory"`
	Binding          string      `json:"binding"`
	RunWorkerFirst   interface{} `json:"run_worker_first"`
	NotFoundHandling string      `json:"not_found_handling"`
	HTMLHandling     string      `json:"html_handling"`
}

// wranglerFileNames are checked in order; the first one found wins, matching
// wrangler's own precedence for a single project directory.
var wranglerFileNames = []string{"wrangler.jsonc", "wrangler.json", "wrangler.toml"}

// parseWranglerConfig parses whichever supported config file is present in the
// repository root. It returns nil when the repository has none, which keeps
// repositories that only use this platform's own layout working unchanged.
func parseWranglerConfig(files map[string]string) (*wranglerConfig, string, error) {
	for _, name := range wranglerFileNames {
		raw, ok := files[name]
		if !ok {
			continue
		}
		var (
			cfg *wranglerConfig
			err error
		)
		if strings.HasSuffix(name, ".toml") {
			cfg, err = parseWranglerTOML(raw)
		} else {
			cfg, err = parseWranglerJSON(raw)
		}
		if err != nil {
			return nil, name, fmt.Errorf("%s: %v", name, err)
		}
		return cfg, name, nil
	}
	return nil, "", nil
}

// parseWranglerJSON handles wrangler.json and wrangler.jsonc. JSONC is the
// format wrangler recommends, so comments and trailing commas are legal input;
// hujson implements that dialect rather than a hand-rolled scanner.
func parseWranglerJSON(raw string) (*wranglerConfig, error) {
	standard, err := hujson.Standardize([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("not valid JSON/JSONC: %v", err)
	}
	var doc struct {
		Main               string              `json:"main"`
		CompatibilityDate  string              `json:"compatibility_date"`
		CompatibilityFlags []string            `json:"compatibility_flags"`
		Assets             *wranglerJSONAssets `json:"assets"`
	}
	if err := json.Unmarshal(standard, &doc); err != nil {
		return nil, fmt.Errorf("not valid JSON/JSONC: %v", err)
	}
	var assets *wranglerAssets
	if doc.Assets != nil {
		assets = &wranglerAssets{
			Directory:        doc.Assets.Directory,
			Binding:          doc.Assets.Binding,
			NotFoundHandling: doc.Assets.NotFoundHandling,
			HTMLHandling:     doc.Assets.HTMLHandling,
			RunWorkerFirst:   encodeRunWorkerFirst(doc.Assets.RunWorkerFirst),
		}
	}
	return &wranglerConfig{
		Main:               doc.Main,
		CompatibilityDate:  doc.CompatibilityDate,
		CompatibilityFlags: doc.CompatibilityFlags,
		Assets:             assets,
	}, nil
}

// encodeRunWorkerFirst keeps the union value (bool or []string) in the compact
// JSON form the rest of the pipeline consumes.
func encodeRunWorkerFirst(value interface{}) json.RawMessage {
	switch v := value.(type) {
	case nil:
		return nil
	case bool:
		if v {
			return json.RawMessage("true")
		}
		return json.RawMessage("false")
	case []interface{}:
		list := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				list = append(list, s)
			}
		}
		if encoded, err := json.Marshal(list); err == nil {
			return encoded
		}
	}
	return nil
}

// parseWranglerTOML uses the standard TOML decoder. Only the keys we consume are
// declared, so unrelated wrangler settings are ignored instead of rejected.
func parseWranglerTOML(raw string) (*wranglerConfig, error) {
	var doc struct {
		Main               string              `toml:"main"`
		CompatibilityDate  string              `toml:"compatibility_date"`
		CompatibilityFlags []string            `toml:"compatibility_flags"`
		Assets             *wranglerTOMLAssets `toml:"assets"`
	}
	if _, err := toml.Decode(raw, &doc); err != nil {
		return nil, fmt.Errorf("not valid TOML: %v", err)
	}
	return &wranglerConfig{
		Main:               doc.Main,
		CompatibilityDate:  doc.CompatibilityDate,
		CompatibilityFlags: doc.CompatibilityFlags,
		Assets:             doc.Assets.wranglerAssets(),
	}, nil
}

// wranglerTOMLAssets decodes run_worker_first as either a boolean or a string
// array. TOML is statically typed, so the union needs an intermediate form.
type wranglerTOMLAssets struct {
	Directory        string      `toml:"directory"`
	Binding          string      `toml:"binding"`
	RunWorkerFirst   interface{} `toml:"run_worker_first"`
	NotFoundHandling string      `toml:"not_found_handling"`
	HTMLHandling     string      `toml:"html_handling"`
}

func (a *wranglerTOMLAssets) wranglerAssets() *wranglerAssets {
	if a == nil {
		return nil
	}
	out := &wranglerAssets{
		Directory:        a.Directory,
		Binding:          a.Binding,
		NotFoundHandling: a.NotFoundHandling,
		HTMLHandling:     a.HTMLHandling,
	}
	switch v := a.RunWorkerFirst.(type) {
	case bool:
		out.RunWorkerFirst = json.RawMessage("false")
		if v {
			out.RunWorkerFirst = json.RawMessage("true")
		}
	case []interface{}:
		list := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				list = append(list, s)
			}
		}
		if encoded, err := json.Marshal(list); err == nil {
			out.RunWorkerFirst = encoded
		}
	case []string:
		if encoded, err := json.Marshal(v); err == nil {
			out.RunWorkerFirst = encoded
		}
	}
	return out
}

// workerCompatibilityDate is validated loosely: wrangler requires yyyy-mm-dd,
// and accepting anything else would silently change runtime behaviour.
const workerMinimumCompatibilityDate = "2021-11-02"
const workerDefaultCompatibilityDate = "2026-09-28"

var workerCompatibilityDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var workerCompatibilityFlag = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// normalizeWorkerCompatibility makes runtime semantics immutable build input.
// The upper bound tracks the workerd version shipped by workers/runtime; a
// future compatibility date cannot be promised until that binary is upgraded.
func normalizeWorkerCompatibility(date string, flags []string) (string, []string, error) {
	if date == "" {
		date = workerDefaultCompatibilityDate
	}
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || !workerCompatibilityDate.MatchString(date) || parsed.Format("2006-01-02") != date {
		return "", nil, fmt.Errorf("compatibility_date must be a valid yyyy-mm-dd date")
	}
	if date > workerDefaultCompatibilityDate {
		return "", nil, fmt.Errorf("compatibility_date %s is newer than the supported workerd date %s", date, workerDefaultCompatibilityDate)
	}
	if date < workerMinimumCompatibilityDate {
		return "", nil, fmt.Errorf("compatibility_date %s is older than the Workers runtime minimum %s", date, workerMinimumCompatibilityDate)
	}
	if len(flags) > 32 {
		return "", nil, fmt.Errorf("at most 32 compatibility_flags are supported")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(flags))
	for _, flag := range flags {
		if !workerCompatibilityFlag.MatchString(flag) {
			return "", nil, fmt.Errorf("invalid compatibility flag %q", flag)
		}
		if seen[flag] {
			return "", nil, fmt.Errorf("duplicate compatibility flag %q", flag)
		}
		seen[flag] = true
		out = append(out, flag)
	}
	sort.Strings(out)
	return date, out, nil
}

func resolveWranglerCompatibility(cfg *wranglerConfig) (string, []string, error) {
	if cfg == nil {
		return normalizeWorkerCompatibility("", nil)
	}
	return normalizeWorkerCompatibility(cfg.CompatibilityDate, cfg.CompatibilityFlags)
}

// resolveWranglerBuild turns a parsed wrangler config into the build parameters
// this platform needs, mapping wrangler's paths onto our repository layout.
// Wrangler is the sole source of truth for repository entrypoints and assets;
// the repository build API deliberately exposes no per-build overrides.
func resolveWranglerBuild(cfg *wranglerConfig) (WorkerAssetsConfig, string, error) {
	var out WorkerAssetsConfig
	var entry string
	if cfg != nil {
		if cfg.Main != "" {
			// wrangler's `main` is relative to the project root; our bundle strips
			// the project root, so the same relative path works unchanged.
			entry = path.Clean(cfg.Main)
			if strings.HasPrefix(entry, "../") || entry == ".." {
				return out, "", fmt.Errorf("wrangler main must stay inside the project directory")
			}
			entry = strings.TrimPrefix(entry, "./")
		}
		if cfg.Assets != nil {
			wa := cfg.Assets
			if wa.Directory != "" {
				out.Directory = strings.TrimPrefix(path.Clean(wa.Directory), "./")
			}
			out.Binding = wa.Binding
			if len(wa.RunWorkerFirst) > 0 {
				var boolValue bool
				if err := json.Unmarshal(wa.RunWorkerFirst, &boolValue); err == nil {
					// wrangler: true means "run the Worker first for every request".
					if boolValue {
						out.RunWorkerFirst = []string{"/*"}
					}
				} else {
					var list []string
					if err := json.Unmarshal(wa.RunWorkerFirst, &list); err == nil {
						out.RunWorkerFirst = list
					}
				}
			}
			out.NotFoundHandling = wa.NotFoundHandling
			out.HTMLHandling = wa.HTMLHandling
		}
	}
	if out.Directory != "" {
		out.Directory = strings.TrimPrefix(path.Clean(out.Directory), "./")
		if strings.HasPrefix(out.Directory, "../") || out.Directory == ".." {
			return out, "", fmt.Errorf("assets directory must stay inside the project directory")
		}
	}
	return out, entry, nil
}

// validateWranglerInputAssets validates the fields that are known before a
// repository build. Cloudflare's Vite plugin may declare only an assets
// binding initially and generate the concrete directory in
// .wrangler/deploy/config.json, so the final directory requirement belongs to
// the artifact validator rather than this pre-build check.
func validateWranglerInputAssets(c WorkerAssetsConfig) error {
	if c.Directory == "" {
		c.Directory = "__wrangler_generated_assets__"
	}
	return validateWorkerAssets(&c)
}
