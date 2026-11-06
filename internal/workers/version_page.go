package workers

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

// VersionPageOptions applies after environment filtering and authorization.
// Callers using another protocol can omit it to retain that protocol's list shape.
type VersionPageOptions struct {
	Limit  int
	After  string
	Search string
}

type versionPageCursor struct {
	Created time.Time `json:"created_at"`
	ID      string    `json:"id"`
}

func versionPage[T any](items []T, options []VersionPageOptions, describe func(T) (time.Time, string, string)) (any, Outcome, error) {
	if len(options) == 0 {
		return collection(items)
	}
	option := options[0]
	if option.Limit < 1 || option.Limit > 200 {
		return nil, Read, domain.Problem(422, "PAGE_INVALID", "limit must be between 1 and 200")
	}
	var after versionPageCursor
	if option.After != "" {
		raw, err := base64.RawURLEncoding.DecodeString(option.After)
		if err != nil || json.Unmarshal(raw, &after) != nil || after.ID == "" || after.Created.IsZero() {
			return nil, Read, domain.Problem(422, "PAGE_INVALID", "Invalid page cursor")
		}
	}
	search := strings.ToLower(strings.TrimSpace(option.Search))
	page := make([]T, 0, option.Limit)
	next := ""
	for _, item := range items {
		created, id, text := describe(item)
		if option.After != "" && (created.After(after.Created) || created.Equal(after.Created) && id <= after.ID) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(text), search) {
			continue
		}
		if len(page) == option.Limit {
			stamp, lastID, _ := describe(page[len(page)-1])
			raw, _ := json.Marshal(versionPageCursor{Created: stamp, ID: lastID})
			next = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		page = append(page, item)
	}
	return map[string]any{"items": page, "next_after": next}, Read, nil
}
