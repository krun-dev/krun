package resourcename

import (
	"strings"

	"github.com/gosimple/slug"
	"github.com/krun-dev/krun/internal/domain"
	"golang.org/x/text/unicode/norm"
)

const (
	OrganizationMax = 32
	ResourceMax     = 63
)

// Make turns a display name or a caller-selected name into a stored ASCII path segment.
func Make(value string, min, max int) (string, error) {
	value = norm.NFKC.String(value)
	value = slug.Make(strings.ReplaceAll(value, "_", "-"))
	if len(value) > max {
		value = strings.TrimRight(value[:max], "-")
	}
	if len(value) < min {
		return "", domain.Problem(422, "LOCATOR_INVALID", "Choose a short name containing letters or numbers")
	}
	return value, nil
}
