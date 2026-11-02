package config

import (
	"strconv"
)

// localBuilderEnvironment deliberately does not inherit the control plane's
// environment. In particular HOME, npm_config_* and cloud credentials must
// never become available to a build started by an untrusted tenant. A small
// allowlist keeps executable lookup, corporate TLS roots and an explicitly
// configured transport proxy working without exposing unrelated secrets.
func WorkerBuilderCommandTimeoutMillis(seconds int) string {
	if seconds == 0 {
		seconds = 600
	}
	// Leave time for packing, upload and process shutdown. Small configured
	// deadlines still get a usable child timeout; the outer context/Job is the
	// authoritative total deadline.
	seconds -= 30
	if seconds < 30 {
		seconds = 30
	}
	return strconv.Itoa(seconds * 1000)
}
