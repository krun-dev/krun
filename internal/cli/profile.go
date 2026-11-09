package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/krun-dev/krun/internal/configfile"
)

func profilePath(selection string) (string, error) {
	if selection == "" {
		return defaultPath(), nil
	}
	if strings.ContainsAny(selection, "/\\") {
		if err := configfile.ValidatePath(selection); err != nil {
			return "", fmt.Errorf("--profile: %w", err)
		}
		return selection, nil
	}
	if !organizationID.MatchString(selection) {
		return "", errors.New("--profile must be a name or an explicit YAML path such as ./team.yaml")
	}
	return filepath.Join(filepath.Dir(defaultPath()), "profiles", selection+".yaml"), nil
}
