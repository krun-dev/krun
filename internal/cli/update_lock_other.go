//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package cli

import "os"

func lockCLIUpdateStamp(*os.File) bool {
	return false
}
