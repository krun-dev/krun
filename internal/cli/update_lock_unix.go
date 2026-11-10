//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockCLIUpdateStamp(file *os.File) bool {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}
