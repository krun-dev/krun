package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	"github.com/krun-dev/krun/internal/config"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestExternalBucketLeavesBucketAndPolicyUntouched(t *testing.T) {
	// No S3 client is deliberately installed: external mode must not issue either
	// bucket administration or policy requests, even on a first deployment.
	controller := workerapp.Controller{
		Config: &config.WorkersConfig{
			S3BucketMode: "external",
			S3Prefix:     "dev/run",
		},
	}
	if err := controller.EnsureBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.EnsureAssetPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type policyRecorder struct {
	objectstore.Client
	policy  string
	created bool
}

func (c *policyRecorder) BucketExists(context.Context, string) (bool, error) {
	return c.created, nil
}

func (c *policyRecorder) MakeBucket(context.Context, string, objectstore.MakeBucketOptions) error {
	c.created = true
	return nil
}

func (c *policyRecorder) SetBucketPolicy(_ context.Context, _ string, policy string) error {
	c.policy = policy
	return nil
}
func TestManagedAssetPolicyScopesItsConfiguredPrefix(t *testing.T) {
	client := &policyRecorder{}
	controller := workerapp.Controller{
		S3:     client,
		Config: &config.WorkersConfig{S3Bucket: "bucket", S3Prefix: "dev/run", S3BucketMode: "managed"},
	}
	if err := controller.EnsureAssetPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Statements []struct {
			Resource []string `json:"Resource"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(client.policy), &policy); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"arn:aws:s3:::bucket/dev/run/pub/*",
		"arn:aws:s3:::bucket/dev/run/int/*",
		"arn:aws:s3:::bucket/dev/run/config/*",
	}
	if len(policy.Statements) != 1 || len(policy.Statements[0].Resource) != 3 {
		t.Fatal("invalid policy scope")
	}
	for i, value := range want {
		if policy.Statements[0].Resource[i] != value {
			t.Fatal("policy escaped configured prefix")
		}
	}
}

func TestS3AccessModeDoesNotDependOnBucketManagement(t *testing.T) {
	for _, test := range []struct {
		name       string
		bucketMode string
		accessMode string
		wantPolicy bool
	}{
		{"managed direct", "managed", "direct", true},
		{"managed signed", "managed", "signed", false},
		{"external direct", "external", "direct", false},
		{"external signed", "external", "signed", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &policyRecorder{}
			controller := workerapp.Controller{
				S3: client,
				Config: &config.WorkersConfig{
					S3Bucket:     "bucket",
					S3BucketMode: test.bucketMode,
					S3AccessMode: test.accessMode,
				},
			}
			if err := controller.EnsureBucket(context.Background()); err != nil {
				t.Fatal(err)
			}
			if client.created != (test.bucketMode == "managed") {
				t.Fatal("bucket creation does not match bucket management mode")
			}
			if got := client.policy != ""; got != test.wantPolicy {
				t.Fatalf("anonymous bucket policy written = %v, want %v", got, test.wantPolicy)
			}
			wantPrefix := "/bucket/pub/"
			if test.accessMode == "signed" {
				wantPrefix = "/"
			}
			if got := controller.Config.StaticRoutePrefix("pub"); got != wantPrefix {
				t.Fatalf("static prefix = %q, want %q", got, wantPrefix)
			}
		})
	}
}
