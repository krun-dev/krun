package config

import (
	"testing"
)

func TestS3ObjectAddressing(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		style    string
		bucket   string
		want     string
	}{
		{
			name:     "AWS automatic virtual host",
			endpoint: "https://s3.eu-west-1.amazonaws.com",
			style:    "auto",
			bucket:   "artifacts",
			want:     "https://artifacts.s3.eu-west-1.amazonaws.com/dev/run/config/latest.json",
		},
		{
			name:     "AWS explicit path",
			endpoint: "https://s3.eu-west-1.amazonaws.com",
			style:    "path",
			bucket:   "artifacts",
			want:     "https://s3.eu-west-1.amazonaws.com/artifacts/dev/run/config/latest.json",
		},
		{
			name:     "AWS dotted bucket uses path for TLS",
			endpoint: "https://s3.us-east-1.amazonaws.com",
			style:    "auto",
			bucket:   "my.artifacts",
			want:     "https://s3.us-east-1.amazonaws.com/my.artifacts/dev/run/config/latest.json",
		},
		{
			name:     "MinIO automatic path",
			endpoint: "http://192.0.2.20:9000",
			style:    "auto",
			bucket:   "artifacts",
			want:     "http://192.0.2.20:9000/artifacts/dev/run/config/latest.json",
		},
		{
			name:     "custom S3 automatic path",
			endpoint: "https://storage.example.com:9443",
			style:    "auto",
			bucket:   "artifacts",
			want:     "https://storage.example.com:9443/artifacts/dev/run/config/latest.json",
		},
		{
			name:     "custom S3 explicit virtual host",
			endpoint: "https://storage.example.com:9443",
			style:    "virtual",
			bucket:   "artifacts",
			want:     "https://artifacts.storage.example.com:9443/dev/run/config/latest.json",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := WorkersConfig{
				S3Bucket:          test.bucket,
				S3Prefix:          "dev/run",
				S3AddressingStyle: test.style,
			}
			if got := cfg.S3ObjectURL(test.endpoint, "config/latest.json"); got != test.want {
				t.Fatalf("object URL = %q, want %q", got, test.want)
			}
		})
	}
}
