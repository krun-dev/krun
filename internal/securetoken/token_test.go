package securetoken

import (
	"regexp"
	"testing"
)

func TestNewIDFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^usr-[0-9a-f]{24}$`)
	for range 64 {
		id := NewID("usr")
		if !pattern.MatchString(id) || !IsID(id, "usr") || IsID(id, "grp") {
			t.Fatalf("unexpected user ID %q", id)
		}
	}
}
