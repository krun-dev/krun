// Package upstream builds HTTP transports for trusted outbound calls.
//
// The transport refuses proxies, redirects and any host/address outside the
// policy supplied by the caller. Policies stay with the adapter that owns the
// upstream contract; only the dialing and response hardening are shared.
package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

var ErrRedirect = errors.New("upstream redirects disabled")

type Policy struct {
	CheckHost func(host, port string) error
	CheckIP   func(net.IP) error
}

func NewTransport(policy Policy) *http.Transport {
	if policy.CheckHost == nil || policy.CheckIP == nil {
		panic("upstream transport requires host and IP policies")
	}
	return &http.Transport{
		Proxy:                  nil,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if err = policy.CheckHost(host, port); err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(addresses) == 0 {
				return nil, errors.New("no upstream addresses")
			}
			for _, address := range addresses {
				if err = policy.CheckIP(address.IP); err != nil {
					return nil, err
				}
			}
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
		},
	}
}

func RejectRedirect(*http.Request, []*http.Request) error { return ErrRedirect }
