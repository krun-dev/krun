package resourcename

import (
	"regexp"
	"strings"

	"github.com/gosimple/slug"
	"github.com/krun-dev/krun/internal/domain"
	"golang.org/x/text/unicode/norm"
)

var workerName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// WorkerValid requires a single lowercase DNS label.
func WorkerValid(name string) bool { return workerName.MatchString(name) }

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
