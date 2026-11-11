package cli

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/krun-dev/krun/internal/securetoken"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

const cliUpdateInterval = 24 * time.Hour
const cliUpdateTimeout = 500 * time.Millisecond

func terminalStream(stream any) bool {
	file, ok := stream.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func calendarVersion(value string) ([3]int, bool) {
	var version [3]int
	parts := strings.Split(value, ".")
	if len(parts) != len(version) || len(parts[0]) != 4 || len(parts[2]) > 9 {
		return version, false
	}
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 1 || strconv.Itoa(number) != part {
			return version, false
		}
		version[i] = number
	}
	return version, version[1] <= 12
}

// Claim the check before sending the request. The lock is released on close,
// including after an interrupted process. Failed requests are throttled too.
func claimCLIUpdateCheck(cacheDir, endpoint string, now time.Time) bool {
	dir := filepath.Join(cacheDir, "krun", "cli-update")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false
	}
	path := filepath.Join(dir, securetoken.Digest(endpoint))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false
	}
	defer file.Close()
	if !lockCLIUpdateStamp(file) {
		return false
	}

	var lastCheck int64
	if binary.Read(file, binary.LittleEndian, &lastCheck) == nil {
		age := now.Sub(time.Unix(0, lastCheck))
		if age > -cliUpdateInterval && age < cliUpdateInterval {
			return false
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false
	}
	return binary.Write(file, binary.LittleEndian, now.UnixNano()) == nil
}

func (c *Client) notifyCLIUpdate(ctx context.Context, cacheDir string, now time.Time) {
	installed, ok := calendarVersion(Version)
	if !ok || ctx.Err() != nil || validateEndpoint(c.Profile.Endpoint) != nil {
		return
	}
	name, err := cliDownloadName()
	if err != nil {
		return
	}
	endpoint := strings.TrimRight(c.Profile.Endpoint, "/")
	if !claimCLIUpdateCheck(cacheDir, endpoint, now) {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, cliUpdateTimeout)
	defer cancel()
	published, _, err := c.publishedCLI(ctx, name)
	if err != nil {
		return
	}
	available, ok := calendarVersion(published)
	if ok && slices.Compare(available[:], installed[:]) > 0 {
		fmt.Fprintf(c.Err, "krun %s is available. Run krun upgrade --endpoint %q to update.\n", published, endpoint)
	}
}
