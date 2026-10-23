package control

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

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
		Main               string         `toml:"main"`
		CompatibilityDate  string         `toml:"compatibility_date"`
		CompatibilityFlags []string       `toml:"compatibility_flags"`
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
var workerCompatibilityDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// resolveWranglerBuild turns a parsed wrangler config into the build parameters
// this platform needs, mapping wrangler's paths onto our repository layout.
//
// Explicit request fields always win; wrangler only supplies defaults. This is
// what makes a standard Cloudflare repository buildable without editing it, and
// still lets a user override the entrypoint or asset directory per build.
func resolveWranglerBuild(cfg *wranglerConfig, assets *WorkerAssetsConfig, entrypoint string) (WorkerAssetsConfig, string, error) {
	var out WorkerAssetsConfig
	if assets != nil {
		out = *assets
	}
	entry := entrypoint
	if cfg != nil {
		if entry == "" && cfg.Main != "" {
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
			if out.Directory == "" && wa.Directory != "" {
				out.Directory = strings.TrimPrefix(path.Clean(wa.Directory), "./")
			}
			if out.RunWorkerFirst == nil && len(wa.RunWorkerFirst) > 0 {
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
			if out.NotFoundHandling == "" {
				out.NotFoundHandling = wa.NotFoundHandling
			}
			if out.HTMLHandling == "" {
				out.HTMLHandling = wa.HTMLHandling
			}
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
