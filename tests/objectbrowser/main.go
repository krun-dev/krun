// Disposable browser-test fixture server. No production entrypoint or alternate application YAML.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/control"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	configPath := flag.String("config", "", "Existing main YAML; the application database is never modified")
	endpointFlag := flag.String("endpoint", "", "Disposable loopback MinIO endpoint")
	databaseURL := flag.String("database-url", "", "Optional disposable PostgreSQL URI for this test fixture")
	flag.Parse()
	if *configPath == "" || *endpointFlag == "" {
		return fmt.Errorf("-config and -endpoint are required")
	}
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load main configuration failed")
	}
	if *databaseURL != "" {
		cfg.DB = *databaseURL
	}
	db, err := pgx.Connect(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("connect test database failed")
	}
	defer db.Close(context.Background())
	schema := "object_browser_ui_" + fmt.Sprint(time.Now().UnixNano())
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("create isolated schema failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, cleanupErr := db.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); cleanupErr != nil {
			fmt.Fprintln(os.Stderr, "isolated browser-test schema cleanup failed:", schema)
		}
	}()
	connection, err := url.Parse(cfg.DB)
	if err != nil || (connection.Scheme != "postgres" && connection.Scheme != "postgresql") {
		return fmt.Errorf("test harness requires a PostgreSQL URI")
	}
	parameters := connection.Query()
	parameters.Set("search_path", schema)
	connection.RawQuery = parameters.Encode()
	cfg.DB = connection.String()
	probe, err := pgx.Connect(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("connect isolated schema failed")
	}
	var actualSchema string
	err = probe.QueryRow(ctx, "SELECT current_schema()").Scan(&actualSchema)
	probe.Close(ctx)
	if err != nil || actualSchema != schema {
		return fmt.Errorf("test schema isolation failed")
	}
	if err := configureFixtureStorage(ctx, &cfg, *endpointFlag); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	cfg.Listen = listener.Addr().String()
	cfg.PublicURL = "http://" + cfg.Listen
	cfg.Development = true
	cfg.OrganizationSelfService = true
	cfg.Providers = []config.ProviderConfig{
		{ID: "ui-dev", Source: "ui-dev", Type: "dev", Name: "本地开发登录", Registration: "jit"},
		// Distinct placeholder client namespaces keep the fixed dev subject in
		// separate identities, using the real source-registration rules.
		{
			ID:           "ui-member",
			Source:       "ui-member",
			Type:         "dev",
			ClientID:     "replace-me-ui-member",
			Name:         "普通成员测试登录",
			Registration: "jit",
		},
		{
			ID:           "ui-viewer",
			Source:       "ui-viewer",
			Type:         "dev",
			ClientID:     "replace-me-ui-viewer",
			Name:         "只读成员测试登录",
			Registration: "jit",
		},
		{
			ID:           "ui-invitee",
			Source:       "ui-invitee",
			Type:         "dev",
			ClientID:     "replace-me-ui-invitee",
			Name:         "邀请成员测试登录",
			Registration: "jit",
		},
		{
			ID:           "ui-outsider",
			Source:       "ui-outsider",
			Type:         "dev",
			ClientID:     "replace-me-ui-outsider",
			Name:         "邀请边界测试登录",
			Registration: "jit",
		},
	}
	cfg.StaticDir, _ = filepath.Abs("dist/console")
	store, err := control.OpenStore(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("initialize isolated storage failed: %w", err)
	}
	defer store.Close()
	server, err := control.NewServer(ctx, cfg, store)
	if err != nil {
		return fmt.Errorf("initialize server failed: %w", err)
	}
	user := securetoken.NewID("usr")
	err = store.Update(ctx, control.PartitionAll, func(st *domain.State) error {
		st.Users[user] = domain.User{ID: user, Source: "ui-dev", Subject: "developer", Name: "UI R2 admin", Status: "active"}
		return nil
	})
	if err != nil {
		return err
	}
	if err = control.Bootstrap(ctx, store, user); err != nil {
		return err
	}
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = httpServer.Close() }()
	fmt.Println("UI_ORIGIN=" + cfg.PublicURL)
	if err = httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
func configureFixtureStorage(ctx context.Context, cfg *config.Config, endpointValue string) error {
	endpoint, err := url.Parse(endpointValue)
	if err != nil || endpoint.Hostname() != "127.0.0.1" || endpoint.Scheme != "http" ||
		endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" {
		return fmt.Errorf("local test storage must be loopback")
	}
	if cfg.Workers == nil {
		return fmt.Errorf("main configuration must include workers")
	}
	sdk, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4("fixture-access", "fixture-secret-browser-only", ""),
		Region: "us-east-1",
	})
	if err != nil {
		return fmt.Errorf("initialize disposable MinIO client failed")
	}
	// A new disposable container is mandatory; do not reuse someone else's bucket.
	if err := sdk.MakeBucket(ctx, "krun-ui-fixture", minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
		return fmt.Errorf("create isolated MinIO bucket failed")
	}
	cfg.Workers.ObjectStorage = &config.WorkerObjectStorageConfig{
		DefaultStore: "local",
		CursorKey:    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Stores: []config.WorkerObjectStore{{
			ID:              "local",
			Endpoint:        endpoint.String(),
			Region:          "us-east-1",
			Bucket:          "krun-ui-fixture",
			RootPrefix:      "worker-data/v1",
			AddressingStyle: "path",
			Credentials: config.WorkerStorageCredentialConfig{
				Type:            "static",
				AccessKeyID:     "fixture-access",
				SecretAccessKey: "fixture-secret-browser-only",
			},
		}},
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
