package postgresproxy

import (
	"net/netip"
	"testing"
)

func TestOriginPoolRanges(t *testing.T) {
	protected := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32"), netip.MustParsePrefix("10.0.0.10/32")}
	for _, test := range []struct {
		address         string
		public, private bool
		allowed         bool
	}{
		{"203.0.113.2", true, false, true},
		{"203.0.113.2", false, true, false},
		{"10.0.0.20", true, false, false},
		{"10.0.0.20", false, true, true},
		{"100.64.1.2", true, false, false},
		{"fd00::1", false, true, true},
		{"127.0.0.1", true, true, false},
		{"169.254.169.254", true, true, false},
		{"192.0.2.10", true, true, false},
		{"10.0.0.10", true, true, false},
	} {
		if got := permitsOrigin(netip.MustParseAddr(test.address), test.public, test.private, protected); got != test.allowed {
			t.Errorf("%s public=%t private=%t: got %t", test.address, test.public, test.private, got)
		}
	}
}
