package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/krun-dev/krun/internal/adapters/postgresproxy"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/pgwire"
)

func (s *Server) startHyperdrive(ctx context.Context, cancel context.CancelFunc) (<-chan error, error) {
	done := make(chan error, 1)
	if s.Config.Workers == nil || s.Config.Workers.Hyperdrive == nil {
		close(done)
		return done, nil
	}
	cfg := s.Config.Workers.Hyperdrive
	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(cfg.Address)
	if cert.Leaf == nil || cert.Leaf.VerifyHostname(host) != nil {
		return nil, errors.New("Hyperdrive TLS certificate does not match its runtime address")
	}
	var roots *x509.CertPool
	if cfg.OriginCAFile != "" {
		raw, err := os.ReadFile(cfg.OriginCAFile)
		if err != nil {
			return nil, err
		}
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("Hyperdrive origin CA file contains no certificates")
		}
	}
	protected := []netip.Prefix{netip.MustParsePrefix(cfg.CIDR())}
	for _, cidr := range s.Config.Workers.ProtectedEgressCIDRs {
		protected = append(protected, netip.MustParsePrefix(cidr))
	}
	for _, listener := range []string{s.Config.Listen, s.Config.InternalListen} {
		host, _, _ := net.SplitHostPort(listener)
		if ip, err := netip.ParseAddr(host); err == nil {
			ip = ip.Unmap()
			protected = append(protected, netip.PrefixFrom(ip, ip.BitLen()))
		}
	}
	app := s.workerApplication(nil)
	resolve := func(ctx context.Context, target domain.HyperdriveConfig, password string, public, private bool) (pgwire.Origin, error) {
		address, err := postgresproxy.ResolveOrigin(ctx, target, public, private, protected)
		if err != nil {
			return pgwire.Origin{}, err
		}
		return pgwire.Origin{
			ID: address.String(), Host: target.Host, Address: address,
			Database: target.Database, User: target.User, Password: password, RootCAs: roots, Limit: 16,
		}, nil
	}
	proxy := pgwire.New(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert},
	}, func(ctx context.Context, user, token string) (pgwire.Origin, error) {
		target, password, public, private, err := app.ResolveHyperdriveSession(ctx, user, token)
		if err != nil {
			return pgwire.Origin{}, err
		}
		return resolve(ctx, target, password, public, private)
	})
	if s.workers != nil {
		s.workers.ValidateHyperdrive = func(ctx context.Context, version domain.WorkerVersion, pool appconfig.WorkerRuntimePool) error {
			ctx, stop := context.WithTimeout(ctx, 8*time.Second)
			defer stop()
			for _, id := range version.HyperdriveBindings {
				target, password, err := app.ResolveHyperdriveOrigin(ctx, version.Organization, id)
				if err != nil {
					return err
				}
				origin, err := resolve(ctx, target, password, pool.PublicEgress == "direct", pool.PrivateEgress == "direct")
				if err != nil {
					return err
				}
				if err := pgwire.Probe(ctx, origin); err != nil {
					return err
				}
			}
			return nil
		}
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(done)
		if err := proxy.Serve(ctx, listener); err != nil {
			done <- err
			cancel()
		}
	}()
	return done, nil
}
