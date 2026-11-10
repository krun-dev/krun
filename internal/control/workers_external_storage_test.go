package control

import (
	"context"
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
func TestManagedBucketRemovesLegacyAnonymousPolicy(t *testing.T) {
	for _, existing := range []bool{false, true} {
		client := &policyRecorder{policy: "legacy-policy", created: existing}
		controller := workerapp.Controller{
			S3:     client,
			Config: &config.WorkersConfig{S3Bucket: "bucket", S3BucketMode: "managed"},
		}
		if err := controller.EnsureBucket(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !client.created || client.policy != "" {
			t.Fatal("managed bucket did not become private")
		}
	}
}
