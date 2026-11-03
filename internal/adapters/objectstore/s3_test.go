package objectstore

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
)

func TestS3PresignedURLsMatchRuntimeAddressing(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		style    string
		region   string
		bucket   string
	}{
		{"AWS auto", "https://s3.eu-west-1.amazonaws.com", "auto", "eu-west-1", "artifacts"},
		{"AWS China auto", "https://s3.cn-north-1.amazonaws.com.cn", "auto", "cn-north-1", "artifacts"},
		{"AWS dual-stack explicit", "https://s3.dualstack.eu-west-1.amazonaws.com", "auto", "eu-west-1", "artifacts"},
		{"AWS path", "https://s3.eu-west-1.amazonaws.com", "path", "eu-west-1", "artifacts"},
		{"AWS dotted bucket", "https://s3.us-east-1.amazonaws.com", "auto", "us-east-1", "my.artifacts"},
		{"MinIO path", "http://127.0.0.1:9000", "path", "us-east-1", "artifacts"},
		{"MinIO auto", "http://127.0.0.1:9000", "auto", "us-east-1", "artifacts"},
		{"custom path", "https://storage.example.com:9443", "path", "auto", "artifacts"},
		{"custom virtual", "https://storage.example.com:9443", "virtual", "cn-beijing", "artifacts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.WorkersConfig{
				S3Endpoint:        test.endpoint,
				S3Bucket:          test.bucket,
				S3AddressingStyle: test.style,
				S3Region:          test.region,
				S3Prefix:          "dev/run",
				S3AccessKey:       "test-access",
				S3SecretKey:       "test-secret",
			}
			client, err := NewWorkerS3(cfg)
			if err != nil {
				t.Fatal(err)
			}

			const key = "config/space name/latest.json"
			want, err := url.Parse(cfg.S3ObjectURL(test.endpoint, key))
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"GET", "PUT"} {
				var signed *url.URL
				if method == "GET" {
					signed, err = client.PresignedGetObject(context.Background(), test.bucket, key, time.Minute, nil)
				} else {
					signed, err = client.PresignedPutObject(context.Background(), test.bucket, key, time.Minute)
				}
				if err != nil {
					t.Fatal(err)
				}
				if signed.Scheme != want.Scheme || signed.Host != want.Host || signed.EscapedPath() != want.EscapedPath() {
					t.Fatalf("%s SDK URL %s disagrees with runtime URL %s", method, signed.Redacted(), want.Redacted())
				}
				credential := signed.Query().Get("X-Amz-Credential")
				if !strings.Contains(credential, "/"+test.region+"/s3/aws4_request") {
					t.Fatalf("%s signature uses incorrect region", method)
				}
			}
		})
	}
}
