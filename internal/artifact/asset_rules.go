package artifact

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/krun-dev/krun/internal/domain"
)

// ValidateAssetRules bounds the immutable rule text. As in Wrangler, malformed
// individual rules are ignored by the runtime rather than executed as code.
func ValidateAssetRules(c *domain.WorkerAssetsConfig) error {
	invalid := func(message string) error { return domain.Problem(422, "WORKER_ASSETS_INVALID", message) }
	if c.BasePath != "" {
		decoded, err := url.PathUnescape(c.BasePath)
		if err != nil || decoded != c.BasePath || len(c.BasePath) > 240 || !strings.HasPrefix(c.BasePath, "/") || strings.HasPrefix(c.BasePath, "//") || path.Clean(c.BasePath) != strings.TrimSuffix(c.BasePath, "/") && c.BasePath != "/" || strings.ContainsAny(c.BasePath, "\\?#\r\n\t") {
			return invalid("assets.base_path must be a canonical absolute URL path")
		}
	}
	for _, rule := range []struct {
		text         string
		limit, count int
	}{{c.Headers, 2000, 100}, {c.Redirects, 1000, 2100}} {
		if len(rule.text) > 2*1024*1024 || !utf8.ValidString(rule.text) || strings.ContainsRune(rule.text, 0) {
			return invalid("Invalid asset rule text")
		}
		count := 0
		for _, line := range strings.Split(rule.text, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if utf8.RuneCountInString(line) > rule.limit {
				return invalid("Asset rule line exceeds the Cloudflare limit")
			}
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") && len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
				count++
			}
		}
		if count > rule.count {
			return invalid("Too many asset rules")
		}
	}
	static, dynamic := 0, 0
	for _, line := range strings.Split(c.Redirects, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if strings.Contains(fields[0], "*") || assetPlaceholder.MatchString(fields[0]) {
			dynamic++
		} else {
			static++
		}
	}
	if static > 2000 || dynamic > 100 {
		return invalid("_redirects supports at most 2000 static and 100 dynamic rules")
	}
	return nil
}

var assetPlaceholder = regexp.MustCompile(`:[A-Za-z]\w*`)
