package workers

import (
	"context"
	"testing"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func TestLocalProjectQueuesWranglerBuildAndReplaysByContent(t *testing.T) {
	st := domain.NewState()
	st.Organizations["org"] = domain.Organization{ID: "org"}
	st.Resources["worker"] = domain.Resource{ID: "worker", Kind: "worker", Organization: "org"}
	st.Workers.Workers["worker"] = domain.Worker{Environments: map[string]domain.WorkerEnvironment{
		"production": {Name: "production", Enabled: true},
	}}
	p := authorization.Principal{User: domain.User{ID: "u", AuthVersion: 2}}
	s := Service{Config: &config.Config{Workers: &config.WorkersConfig{}}, Require: func(_ *domain.State, _ authorization.Principal, action string, target authorization.Target) error {
		if action != "build.start" || target.Environment != "production" {
			t.Fatalf("incorrect authorization target: %s %+v", action, target)
		}
		return nil
	}}
	ref := WorkerRef{Organization: "org", Worker: "worker", Environment: "production"}
	in := LocalBuildSource{
		Key: "retry", SourceKey: WorkerSourceUploadKey("org", "first"), Digest: "same-content",
		ConfigPath: "wrangler.toml", CompatibilityDate: "2026-09-28",
	}
	build, err := s.QueueLocalProject(context.Background(), st, p, ref, in)
	if err != nil || build.State != "queued" || build.Repository != nil || build.SourceDigest != "same-content" {
		t.Fatalf("unexpected build: %+v, %v", build, err)
	}
	private := st.Workers.BuildPrivate[build.ID]
	if private.ConfigPath != "wrangler.toml" || private.SourceKey != in.SourceKey || private.Actor.User != "u" {
		t.Fatalf("unexpected private job input: %+v", private)
	}

	in.SourceKey = WorkerSourceUploadKey("org", "second")
	replay, err := s.QueueLocalProject(context.Background(), st, p, ref, in)
	if err != nil || replay.ID != build.ID || len(st.Workers.Builds) != 1 {
		t.Fatalf("same upload did not reuse its build: %+v, %v", replay, err)
	}
	in.Digest = "different-content"
	if _, err = s.QueueLocalProject(context.Background(), st, p, ref, in); err == nil {
		t.Fatal("different content was accepted with the same idempotency key")
	}
}
