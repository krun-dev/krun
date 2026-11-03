package workers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	authorization "github.com/krun-dev/krun/internal/authorization"
	domain "github.com/krun-dev/krun/internal/domain"
	securetoken "github.com/krun-dev/krun/internal/securetoken"
)

func (s *Service) WorkerArtifactGet(ctx context.Context, st *domain.State, p authorization.Principal, ref ServiceRef) (any, Outcome, error) {
	t, e := s.WorkerCollectionTarget(st, ref, "service.version.read", p)
	if e != nil {
		return nil, Read, e
	}
	a, ok := st.Workers.Artifacts[ref.Item]
	related := false
	for _, b := range st.Workers.Builds {
		if b.Service == t.ID && b.Artifact == a.ID {
			target := t
			target.Environment = b.Environment
			d, e := authorization.Authorize(st, p, "service.version.read", target)
			if e != nil {
				return nil, Read, e
			}
			related = related || d.Allowed
		}
	}
	if !ok || a.Organization != t.Organization || !related {
		return nil, Read, domain.NotFound()
	}
	return a, Read, nil

}

func (w *Controller) FinalizeBuild(ctx context.Context, b domain.WorkerBuild) (domain.WorkerArtifact, error) {
	return w.FinalizeUploadedBuild(ctx, b, w.Replica)
}

func (w *Controller) FinalizeUploadedBuild(ctx context.Context, b domain.WorkerBuild, owner string) (domain.WorkerArtifact, error) {
	raw, e := w.GetObject(ctx, WorkerUploadKey(b))
	if e != nil {
		return domain.WorkerArtifact{}, e
	}
	code, manifest, e := workerartifact.ParseWorkerArchive(raw)
	if e != nil {
		return domain.WorkerArtifact{}, e
	}
	if code.Profile != b.Profile || code.CompatibilityDate != b.CompatibilityDate || !slices.Equal(code.CompatibilityFlags, b.CompatibilityFlags) {
		return domain.WorkerArtifact{}, errors.New("artifact build profile does not match requested compatibility")
	}
	a := domain.WorkerArtifact{ID: "art_" + securetoken.Digest(b.Organization+"/"+code.ArchiveDigest), Organization: b.Organization, State: "finalized", Profile: code.Profile, StorageLocation: w.Config.S3LocationID, Bucket: w.Config.S3Bucket, ArchiveDigest: code.ArchiveDigest, ManifestDigest: manifest, ModuleDigest: code.Digest, CompatibilityDate: code.CompatibilityDate, CompatibilityFlags: append([]string(nil), code.CompatibilityFlags...), Size: len(raw), ObjectKey: "v1/" + b.Organization + "/sha256/" + code.ArchiveDigest + "/bundle.tar.gz", Created: time.Now().UTC()}
	if e := w.Service.Store.ReserveBuildArtifact(ctx, b.ID, owner, a); e != nil {
		return a, e
	}
	if e := w.PutImmutable(ctx, a.ObjectKey, raw); e != nil {
		return a, e
	}
	if e := w.PutImmutable(ctx, WorkerArtifactManifestKey(a), code.ManifestRaw); e != nil {
		return a, e
	}

	if e := w.PutAssetObjects(ctx, b, a, code); e != nil {
		return a, e
	}
	return a, nil
}

// artifactCode stays as the control plane's own integrity check when promoting
// a build and validating a version candidate; runtime cold loads verify the
// same digests locally after downloading via the presigned URL.
func (w *Controller) ArtifactCode(ctx context.Context, a domain.WorkerArtifact) (workerartifact.WorkerCode, error) {
	if a.StorageLocation != w.Config.S3LocationID || a.Bucket != w.Config.S3Bucket {
		return workerartifact.WorkerCode{}, errors.New("artifact storage location mismatch")
	}
	raw, e := w.GetObject(ctx, a.ObjectKey)
	if e != nil {
		return workerartifact.WorkerCode{}, e
	}
	code, manifest, e := workerartifact.ParseWorkerArchive(raw)
	if e != nil {
		return code, e
	}
	if code.Profile != a.Profile || code.ArchiveDigest != a.ArchiveDigest || code.Digest != a.ModuleDigest || code.CompatibilityDate != a.CompatibilityDate || !slices.Equal(code.CompatibilityFlags, a.CompatibilityFlags) || manifest != a.ManifestDigest || len(raw) != a.Size {
		return code, fmt.Errorf("artifact metadata mismatch")
	}
	return code, nil
}
