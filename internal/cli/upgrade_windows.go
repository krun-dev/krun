package cli

import (
	"errors"
	"fmt"
	"os"
)

func replaceCLI(staged, executable string) error {
	// Windows cannot overwrite a running executable. Keep one fixed backup
	// until a later invocation can remove it after this process exits.
	backup := executable + ".old"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("close older krun processes before upgrading: %w", err)
	}
	if err := os.Rename(executable, backup); err != nil {
		return err
	}
	if err := os.Rename(staged, executable); err != nil {
		return errors.Join(err, os.Rename(backup, executable))
	}
	_ = os.Remove(backup)
	return nil
}
