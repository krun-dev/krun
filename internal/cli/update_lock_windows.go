package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockCLIUpdateStamp(file *os.File) bool {
	var region windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &region,
	) == nil
}
