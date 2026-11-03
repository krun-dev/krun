package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	identityprovider "github.com/krun-dev/krun/internal/adapters/identity"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/configfile"
)

func TestLoadYAMLProviderDefaultsAndOverrides(t *testing.T) {
	t.Setenv("TEST_SSO_SECRET", "test-only")
	t.Setenv("KRUN_DATABASE_URL", "postgres://ignored/other")
	raw := `public_url: http://127.0.0.1:5173
development: true
db: postgres://test-only/krun_test
providers:
  - id: github
    source_id: github-test
    type: github
    client_id: test-client
    client_secret: ${TEST_SSO_SECRET}
  - id: gitlab
    source_id: gitlab-test
    type: gitlab
    base_url: https://gitlab.example.com
    client_id: test-client
    client_secret: ${TEST_SSO_SECRET}
  - id: gitlab-api
    source_id: gitlab-api-test
    type: gitlab
    base_url: https://gitlab.example.com
    client_id: test-client
    client_secret: ${TEST_SSO_SECRET}
    scopes: [api]
  - id: oidc
    source_id: oidc-test
    type: oidc
    issuer: https://sso.example.com
    client_id: test-client
    client_secret: ${TEST_SSO_SECRET}
  - id: custom
    source_id: custom-test
    type: oauth2
    client_id: test-client
    client_secret: ${TEST_SSO_SECRET}
    authorize_url: https://sso.example.com/authorize
    token_url: https://sso.example.com/token
    userinfo_url: https://sso.example.com/user
    subject_path: data.id
    userinfo:
      method: POST
      body:
        include_profile: true
      success:
        path: code
        equals: 0
`
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := appconfig.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DB != "postgres://test-only/krun_test" {
		t.Fatal("db did not use the YAML value")
	}
	for i, want := range [][]string{
		{"read:user"},
		{"read_user"},
		{"api"},
		{
			"openid",
			"profile",
			"email",
		},
		nil,
	} {
		if !reflect.DeepEqual(c.Providers[i].Scopes, want) || c.Providers[i].Registration != "jit" {
			t.Fatalf("incorrect provider defaults: %d", i)
		}
	}
	custom := c.Providers[4]
	if custom.UserInfo.Success.Equals != json.Number("0") || custom.UserInfo.Body["include_profile"] != true {
		t.Fatal("userinfo configuration types changed")
	}
	for name, data := range map[string]string{
		"legacy.json":    raw,
		"legacy-db.yaml": strings.Replace(raw, "db: postgres://test-only/krun_test", "database_url_env: KRUN_DATABASE_URL", 1),
		"unknown.yaml":   raw + "unknown_field: true\n",
		"duplicate.yaml": raw + "public_url: http://localhost:5173\n",
		"nested.yaml":    strings.Replace(raw, "client_id: test-client", "client_id: test-client\n    client_secret_env: LEGACY_SECRET", 1),
	} {
		p := filepath.Join(filepath.Dir(path), name)
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := appconfig.LoadConfig(p); err == nil {
			t.Fatalf("invalid config accepted: %s", name)
		}
	}
}

func TestYAMLServerExamples(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://test-only/krun_test")
	for _, name := range []string{
		"KRUN_GITHUB_SECRET",
		"KRUN_GITLAB_SECRET",
		"KRUN_OIDC_SECRET",
		"KRUN_ALIYUN_SECRET",
		"KRUN_COMPANY_SECRET",
	} {
		t.Setenv(name, "test-only")
	}
	t.Setenv("KRUN_SMTP_PASSWORD", "test-only")
	for _, name := range []string{"development.example.yaml", "example.yaml"} {
		t.Run(name, func(t *testing.T) {
			if _, err := appconfig.LoadConfig(filepath.Join("..", "..", "config", name)); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Validate every template while enabled, so disabled examples cannot hide
	// invalid endpoints, missing credentials or SMTP fields.
	c, err := appconfig.LoadConfig(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	for i := range c.Providers {
		if c.Providers[i].IsEnabled() {
			t.Fatalf("example source %s should require explicit opt-in", c.Providers[i].ID)
		}
		if c.Providers[i].Type != "dev" {
			c.Providers[i].Enabled = &enabled
		}
	}
	if err := c.Validate(); err != nil {
		t.Fatal("enabled production examples:", err)
	}
	c.Development = true
	c.Listen = "127.0.0.1:8080"
	c.PublicURL = "http://127.0.0.1:5173"
	for i := range c.Providers {
		c.Providers[i].Enabled = &enabled
	}
	if err := c.Validate(); err != nil {
		t.Fatal("enabled development examples:", err)
	}
	t.Run("disabled sources need no optional secrets", func(t *testing.T) {
		for _, name := range []string{
			"KRUN_GITHUB_SECRET",
			"KRUN_GITLAB_SECRET",
			"KRUN_OIDC_SECRET",
			"KRUN_ALIYUN_SECRET",
			"KRUN_COMPANY_SECRET",
			"KRUN_SMTP_PASSWORD",
		} {
			t.Setenv(name, "")
		}
		if _, err := appconfig.LoadConfig(filepath.Join("..", "..", "config", "example.yaml")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDevelopmentRequiresPostgres(t *testing.T) {
	c := appconfig.Config{
		Development: true,
		PublicURL:   "http://127.0.0.1:8080",
		DB:          "postgres://test-only/krun_test",
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "  ", "postgres://user:PRIVATE_PASSWORD%@db.example.com/krun"} {
		c.DB = invalid
		err := c.Validate()
		if err == nil {
			t.Fatal("invalid db accepted")
		}
		if strings.Contains(err.Error(), "PRIVATE_PASSWORD") {
			t.Fatal("database credentials exposed in diagnostic")
		}
	}
	c.DB = "postgres://test-only/krun_test"
	var decoded appconfig.Config
	if err := configfile.Decode("server.yaml", []byte("data_file: .data/control.json\n"), &decoded); err == nil {
		t.Fatal("removed data_file setting accepted")
	}
}

func TestUnifiedEnvironmentConfiguration(t *testing.T) {
	const dsn = "postgres://test:PRIVATE_PASSWORD@127.0.0.1:25432/krun_test?sslmode=disable"
	t.Setenv("KRUN_DB_TEST", dsn)
	t.Setenv("KRUN_DB_EMPTY", "")
	t.Setenv("SSO_CLIENT_ID", "test-client")
	t.Setenv("SSO_SECRET", "private-client-secret")
	for _, tc := range []struct {
		name, settings string
		valid          bool
	}{
		{"direct", "db: " + dsn, true},
		{"environment", "db: ${KRUN_DB_TEST}", true},
		{"missing", "", false},
		{"empty-environment", "db: ${KRUN_DB_EMPTY}", false},
		{"legacy-field", "db_env: KRUN_DB_TEST", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.yaml")
			raw := "development: true\npublic_url: http://127.0.0.1:8080\n" + tc.settings + "\nproviders:\n  - id: github\n    source_id: github-test\n    type: github\n    client_id: ${SSO_CLIENT_ID}\n    client_secret: ${SSO_SECRET}\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := appconfig.LoadConfig(path)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid database configuration accepted")
				}
				if strings.Contains(err.Error(), "PRIVATE_PASSWORD") || strings.Contains(err.Error(), "private-client-secret") {
					t.Fatal("credentials exposed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.DB != dsn || c.Providers[0].ClientID != "test-client" || c.Providers[0].ClientSecret != "private-client-secret" {
				t.Fatal("environment values not resolved")
			}
			if err = c.Validate(); err != nil {
				t.Fatal("repeated validation failed", err)
			}
			p, err := identityprovider.New(context.Background(), c, c.Providers[0], nil)
			if err != nil || p.Oauth.ClientSecret != "private-client-secret" {
				t.Fatal("resolved secret did not reach OAuth client")
			}
		})
	}
}

func TestExternalObjectStoreConfiguration(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://test-only/krun_test")
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	base := string(raw)
	base = strings.Replace(base, "s3_region: us-east-1", "s3_region: cn-beijing", 1)
	base = strings.Replace(base, "s3_addressing_style: auto", "s3_addressing_style: virtual", 1)
	base = strings.Replace(base, "s3_prefix: \"\"", "s3_prefix: dev/run", 1)
	base = strings.Replace(base, "s3_bucket_mode: managed", "s3_bucket_mode: external", 1)
	base = strings.Replace(base, "  config_base_url: https://s3.internal.example.com/krun-artifacts/config\n", "", 1)
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(file, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := appconfig.LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers.S3Region != "cn-beijing" || cfg.Workers.S3Prefix != "dev/run" || cfg.Workers.S3BucketMode != "external" || cfg.Workers.S3AccessMode != "signed" || cfg.Workers.ConfigBaseURL != "https://krun-artifacts.s3.runtime.internal.example.com/dev/run/config" {
		t.Fatalf("object storage config not loaded correctly")
	}
	for _, test := range []struct{ from, to string }{
		{"s3_region: cn-beijing", "s3_region: invalid/region"},
		{"s3_addressing_style: virtual", "s3_addressing_style: invalid"},
		{"s3_bucket_mode: external", "s3_bucket_mode: invalid"},
		{"s3_access_mode: signed", "s3_access_mode: invalid"},
		{"s3_prefix: dev/run", "s3_prefix: ../other"},
		{"s3_prefix: dev/run", "s3_prefix: .."},
		{"s3_prefix: dev/run", "s3_prefix: /dev/run"},
	} {
		os.WriteFile(file, []byte(strings.Replace(base, test.from, test.to, 1)), 0600)
		if _, err = appconfig.LoadConfig(file); err == nil {
			t.Fatalf("accepted invalid %s", test.to)
		}
	}
}

func TestS3AccessAndBucketModesLoadIndependently(t *testing.T) {
	t.Setenv("KRUN_DATABASE_URL", "postgres://test-only/krun_test")
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, bucketMode := range []string{"managed", "external"} {
		for _, accessMode := range []string{"direct", "signed"} {
			t.Run(bucketMode+"/"+accessMode, func(t *testing.T) {
				text := strings.Replace(string(raw), "s3_bucket_mode: managed", "s3_bucket_mode: "+bucketMode, 1)
				text = strings.Replace(text, "s3_access_mode: signed", "s3_access_mode: "+accessMode, 1)
				file := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(file, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}

				cfg, err := appconfig.LoadConfig(file)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Workers.S3BucketMode != bucketMode || cfg.Workers.S3AccessMode != accessMode {
					t.Fatal("S3 bucket management and access mode were not loaded independently")
				}
				if cfg.Workers.S3UsesSignedAccess() != (accessMode == "signed") {
					t.Fatal("runtime access unexpectedly depends on bucket management")
				}
			})
		}
	}
}
