// Package runtimeconfig serves the runtime read model and acknowledgements.
// Transport credentials and long-poll connection handling belong to HTTP.
package runtimeconfig

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

type Service struct {
	Credentials CredentialSource
	Config      *appconfig.WorkersConfig
	Store       *storage.Store
	Controller  *workerapp.Controller
}

type ChangeBatch struct {
	Epoch        string                `json:"config_epoch"`
	HeadRevision int64                 `json:"config_head_revision"`
	From         int64                 `json:"from"`
	To           int64                 `json:"to"`
	Ops          []domain.EdgeConfigOp `json:"ops"`
}
type Changes struct {
	Resync bool
	Batch  *ChangeBatch
}

func (s *Service) ReadChanges(ctx context.Context, pool, epoch string, since int64) (Changes, error) {
	revisions, floor, head, err := s.Store.ConfigRevisionsSince(context.WithValue(ctx, storage.WorkerRuntimeFeedPoolKey{}, pool), since, 100)
	if err != nil {
		return Changes{}, err
	}
	if epoch != head.Epoch || since > head.Revision || since > 0 && since < floor {
		return Changes{Resync: true}, nil
	}
	if len(revisions) == 0 {
		return Changes{}, nil
	}
	if since == 0 || revisions[0].Revision != since+1 {
		return Changes{Resync: true}, nil
	}
	ops := []domain.EdgeConfigOp{}
	for _, revision := range revisions {
		batch := []domain.EdgeConfigOp{}
		if err := json.Unmarshal(revision.Ops, &batch); err != nil {
			return Changes{}, err
		}
		ops = append(ops, workerapp.EdgeConfigOpsForPool(batch, pool)...)
	}
	if len(ops) > 2000 {
		return Changes{Resync: true}, nil
	}
	return Changes{Batch: &ChangeBatch{
		Epoch:        head.Epoch,
		HeadRevision: head.Revision,
		From:         since,
		To:           revisions[len(revisions)-1].Revision,
		Ops:          ops,
	}}, nil
}

type Artifact struct {
	ID            string                  `json:"artifact_id"`
	ObjectKey     string                  `json:"object_key"`
	Storage       domain.StorageReadGrant `json:"-"`
	ArchiveDigest string                  `json:"archive_sha256"`
	ModuleDigest  string                  `json:"module_sha256"`
	Size          int                     `json:"size"`
}

func (s *Service) Artifact(ctx context.Context, id, pool string) (Artifact, error) {
	out, err := s.Store.RuntimeArtifact(ctx, id, pool)
	if err != nil {
		return Artifact{}, err
	}
	grant, err := s.Controller.ArtifactReadGrant(ctx, out)
	if err != nil {
		return Artifact{}, domain.Unavailable("ARTIFACT_UNAVAILABLE", "Artifact storage unavailable")
	}
	return Artifact{
		ID:            out.ID,
		ObjectKey:     "bundle.tar.gz",
		Storage:       grant,
		ArchiveDigest: out.ArchiveDigest,
		ModuleDigest:  out.ModuleDigest,
		Size:          out.Size,
	}, nil
}

type Secrets struct {
	Version string            `json:"version_id"`
	Digest  string            `json:"secret_digest"`
	Values  map[string]string `json:"values"`
}

func (s *Service) Secrets(ctx context.Context, version, pool string) (Secrets, error) {
	digest, encrypted, err := s.Store.EncryptedVersionSecrets(ctx, version, pool)
	if err != nil {
		return Secrets{}, err
	}
	values, err := workerapp.DecryptWorkerSecrets(s.Config, digest, encrypted)
	if err != nil {
		return Secrets{}, err
	}
	return Secrets{Version: version, Digest: digest, Values: values}, nil
}
func (s *Service) Ack(ctx context.Context, pool string, in domain.WorkerAck) error {
	if in.Pool != pool || in.Pod == "" || in.UID == "" {
		return domain.Invalid("ACK_INVALID", "Pod UID and pool required")
	}
	in.Time = time.Now().UTC()
	return s.Store.RecordAck(ctx, in)
}

type Assets struct {
	Storage *domain.StorageReadGrant            `json:"-"`
	Version string                              `json:"version_id"`
	Config  *domain.WorkerAssetsConfig          `json:"config"`
	Files   []workerartifact.WorkerManifestFile `json:"files"`
}

func (s *Service) Assets(ctx context.Context, version, pool string) (Assets, error) {
	assets, err := s.Controller.VersionAssets(ctx, version, pool)
	if err != nil {
		return Assets{}, err
	}
	out := Assets{Version: assets.Version.ID, Config: assets.Config, Files: assets.Files}
	if assets.Config != nil {
		grant, err := s.AssetCredentials(ctx, version, pool)
		if err != nil {
			return Assets{}, err
		}
		out.Storage = &grant
	}
	return out, nil
}

func (s *Service) AssetCredentials(ctx context.Context, version, poolID string) (domain.StorageReadGrant, error) {
	rel, artifact, err := s.Store.RuntimeVersion(ctx, version, poolID)
	if err != nil {
		return domain.StorageReadGrant{}, err
	}
	pool, ok := s.Config.RuntimePool(poolID)
	if !ok || artifact.StorageLocation != s.Config.S3LocationID || artifact.Bucket != s.Config.S3Bucket {
		return domain.StorageReadGrant{}, domain.NotFound()
	}
	prefix := "int"
	if appconfig.RuntimePoolHasAccess(pool, "public") {
		prefix = "pub"
	}
	key := workerapp.WorkerAssetObjectKey(prefix, rel.Organization, rel.Worker, artifact.ArchiveDigest, "")
	return s.Controller.ReadGrant(ctx, rel.Organization, artifact.ID, key)
}

func (s *Service) Route(ctx context.Context, host, pool string) (workerapp.EdgeConfigSnapshot, error) {
	host = strings.ToLower(host)
	if len(host) > 253 || strings.ContainsAny(host, "/\\: ") {
		return workerapp.EdgeConfigSnapshot{}, domain.NotFound()
	}
	projection, position, err := s.Store.RuntimeConfigRoute(ctx, host, pool)
	return workerapp.EdgeConfigSnapshot{Pool: pool, EdgeConfigCursor: position, Routes: projection.Routes, Versions: projection.Versions}, err
}

// StaticAssetCredentials authorizes the same pub/int visibility as the static
// origin. Its read-only scope includes one immutable site's asset directory.
func (s *Service) StaticAssetCredentials(
	ctx context.Context,
	organization, worker, digest, prefix string,
	pool appconfig.WorkerRuntimePool,
) (domain.StorageReadGrant, error) {
	invalidPrefix := prefix != "pub" && prefix != "int"
	invalidDigest := len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != ""
	invalidOwner := organization == "" || worker == "" || strings.ContainsAny(organization+worker, "/\\")
	if invalidPrefix || invalidDigest || invalidOwner {
		return domain.StorageReadGrant{}, domain.NotFound()
	}
	access := "private"
	if prefix == "pub" {
		access = "public"
	}
	if !appconfig.RuntimePoolHasAccess(pool, access) {
		return domain.StorageReadGrant{}, domain.Forbidden()
	}
	// Builds create both asset copies. A public grant requires an active public
	// route for this digest, even if the same pool also serves private routes.
	if prefix == "pub" {
		hosts, err := s.Store.PublishedAssetHosts(ctx, organization, worker, digest, pool.ID)
		if err != nil {
			return domain.StorageReadGrant{}, err
		}
		public := false
		for _, host := range hosts {
			if routeDomain, ok := s.Config.DomainForHost(host); ok && routeDomain.Access == "public" {
				public = true
				break
			}
		}
		if !public {
			return domain.StorageReadGrant{}, domain.NotFound()
		}
	}
	key := workerapp.WorkerAssetObjectKey(prefix, organization, worker, digest, "")
	return s.Controller.ReadGrant(ctx, organization, worker+"/"+digest, key)
}
