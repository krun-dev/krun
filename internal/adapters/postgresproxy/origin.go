package postgresproxy

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"github.com/krun-dev/krun/internal/domain"
)

var sharedNetwork = netip.MustParsePrefix("100.64.0.0/10")

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

func ResolveOrigin(ctx context.Context, target domain.HyperdriveConfig, allowPublic, allowPrivate bool, protected []netip.Prefix) (netip.AddrPort, error) {
	// Use the host's recursive resolver and its TTL policy. Never dial the
	// original hostname after checking the answer.
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", target.Host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	for _, address := range addresses {
		address = address.Unmap()
		if permitsOrigin(address, allowPublic, allowPrivate, protected) {
			return netip.AddrPortFrom(address, uint16(target.Port)), nil
		}
	}
	return netip.AddrPort{}, errors.New("no database address is allowed by the runtime pool")
}

func permitsOrigin(addr netip.Addr, allowPublic, allowPrivate bool, protected []netip.Prefix) bool {
	if !addr.IsGlobalUnicast() {
		return false
	}
	for _, network := range blockedNetworks {
		if network.Contains(addr) {
			return false
		}
	}
	for _, network := range protected {
		if network.Contains(addr) {
			return false
		}
	}
	if addr.IsPrivate() || sharedNetwork.Contains(addr) {
		return allowPrivate
	}
	return allowPublic
}
