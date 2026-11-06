package workers

import (
	"testing"

	"github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/domain"
)

func TestReservedBindingNamesRejected(t *testing.T) {
	for _, name := range []string{"STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY", "STORAGE_REGION", "STATIC_DOMAINS"} {
		t.Run(name, func(t *testing.T) {
			environments := map[string]domain.WorkerEnvironment{
				"r2": {R2Bindings: map[string]domain.WorkerR2Binding{
					name: {BucketID: "bucket-id", BucketName: "example-bucket"},
				}},
				"assets":   {AssetsBinding: name},
				"metadata": {VersionMetadata: name},
			}
			for kind, env := range environments {
				t.Run(kind, func(t *testing.T) {
					if err := ValidateWorkerBindings(env); err == nil {
						t.Fatalf("accepted reserved %s binding %q", kind, name)
					}
				})
			}
			assets := domain.WorkerAssetsConfig{Directory: "public", Binding: name}
			if err := artifact.ValidateWorkerAssets(&assets); err == nil {
				t.Fatalf("accepted reserved asset configuration %q", name)
			}
		})
	}
}
