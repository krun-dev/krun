//go:build !windows

package cli

import "os"

func replaceCLI(staged, executable string) error {
	return os.Rename(staged, executable)
}
