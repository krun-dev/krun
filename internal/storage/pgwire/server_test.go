package pgwire

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgreSQLRelay(t *testing.T) {
	dsn := os.Getenv("KRUN_TEST_POSTGRES_PROXY_URL")
	if dsn == "" {
		t.Skip("set KRUN_TEST_POSTGRES_PROXY_URL to a TLS-enabled PostgreSQL test database")
	}
	source, err := pgconn.ParseConfig(dsn)
	if err != nil || source.TLSConfig == nil {
		t.Fatal("test PostgreSQL requires a TLS connection")
	}
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", source.Host)
	if err != nil || len(ips) == 0 {
		t.Fatal("test PostgreSQL did not resolve")
	}

	serverTLS, clientTLS := testTLS(t)
	server := New(serverTLS, func(_ context.Context, user, password string) (Origin, error) {
		if user != "binding" || password != "fixture-binding-password" {
			return Origin{}, errors.New("denied")
		}
		return Origin{
			ID:       "fixture",
			Host:     source.Host,
			Address:  netip.AddrPortFrom(ips[0].Unmap(), source.Port),
			User:     source.User,
			Password: source.Password,
			Database: source.Database,
			RootCAs:  source.TLSConfig.RootCAs,
			Limit:    1,
		}, nil
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("proxy shutdown: %v", err)
		}
	})

	client, err := pgx.ParseConfig("postgresql://binding:fixture-binding-password@" + listener.Addr().String() + "/binding?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	client.TLSConfig = clientTLS
	client.Fallbacks = nil
	conn, err := pgx.ConnectConfig(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var value int32
	if err := tx.QueryRow(ctx, "select $1::int + 1", 41).Scan(&value); err != nil || value != 42 {
		t.Fatalf("parameterized query through proxy = %d, %v", value, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pgx.ConnectConfig(ctx, client); !postgresCode(err, "53300") {
		t.Fatalf("second connection should exceed the origin limit: %v", err)
	}
	wrong := client.Copy()
	wrong.Password = "incorrect"
	if _, err := pgx.ConnectConfig(ctx, wrong); !postgresCode(err, "28P01") {
		t.Fatalf("wrong binding password must be rejected: %v", err)
	}

	queryDone := make(chan error, 1)
	go func() {
		_, err := conn.Exec(ctx, "select pg_sleep(10)")
		queryDone <- err
	}()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-queryDone:
			if !postgresCode(err, "57014") {
				t.Fatalf("query was not canceled through the proxy: %v", err)
			}
			return
		case <-ticker.C:
			if err := conn.PgConn().CancelRequest(ctx); err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func postgresCode(err error, code string) bool {
	var postgres *pgconn.PgError
	return errors.As(err, &postgres) && postgres.Code == code
}

func testTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	server := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	client := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "localhost", RootCAs: roots}
	return server, client
}
