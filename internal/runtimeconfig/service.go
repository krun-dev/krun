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
	Config     *appconfig.WorkersConfig
	Store      *storage.Store
	Controller *workerapp.Controller
}

func (s *Service) Head(ctx context.Context) (int64, error) {
	head, _, err := s.Store.ConfigState(ctx)
	return head, err
}

type ChangeBatch struct {
	From int64                   `json:"from"`
	To   int64                   `json:"to"`
	Ops  []domain.WorkerConfigOp `json:"ops"`
}
type Changes struct {
	Resync bool
	Batch  *ChangeBatch
}

func (s *Service) ReadChanges(ctx context.Context, pool string, since int64) (Changes, error) {
	revisions, floor, err := s.Store.ConfigRevisionsSince(context.WithValue(ctx, storage.WorkerRuntimeFeedPoolKey{}, pool), since, 100)
	if err != nil {
		return Changes{}, err
	}
	if since > 0 && since < floor {
		return Changes{Resync: true}, nil
	}
	if len(revisions) == 0 {
		return Changes{}, nil
	}
	if since == 0 || revisions[0].Revision != since+1 {
		return Changes{Resync: true}, nil
	}
	ops := []domain.WorkerConfigOp{}
	for _, revision := range revisions {
		batch := []domain.WorkerConfigOp{}
		if err := json.Unmarshal(revision.Ops, &batch); err != nil {
			return Changes{}, err
		}
		ops = append(ops, workerapp.WorkerConfigOpsForPool(batch, pool)...)
	}
	if len(ops) > 2000 {
		return Changes{Resync: true}, nil
	}
	return Changes{Batch: &ChangeBatch{From: since, To: revisions[len(revisions)-1].Revision, Ops: ops}}, nil
}

type Artifact struct {
	ID            string `json:"artifact_id"`
	ObjectKey     string `json:"object_key"`
	DownloadURL   string `json:"download_url"`
	ArchiveDigest string `json:"archive_sha256"`
	ModuleDigest  string `json:"module_sha256"`
	Size          int    `json:"size"`
}

func (s *Service) Artifact(ctx context.Context, id, pool string) (Artifact, error) {
	out, err := s.Store.RuntimeArtifact(ctx, id, pool)
	if err != nil {
		return Artifact{}, err
	}
	url, err := s.Controller.DownloadURL(ctx, out)
	if err != nil {
		return Artifact{}, domain.Problem(503, "ARTIFACT_UNAVAILABLE", "Artifact storage unavailable")
	}
	return Artifact{ID: out.ID, ObjectKey: out.ObjectKey, DownloadURL: url, ArchiveDigest: out.ArchiveDigest, ModuleDigest: out.ModuleDigest, Size: out.Size}, nil
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
	values := make(map[string]string, len(encrypted))
	versions := make(map[string]string, len(encrypted))
	for _, item := range encrypted {
		value, err := workerapp.DecryptWorkerSecret(s.Config, item)
		if err != nil {
			return Secrets{}, domain.Problem(503, "WORKER_SECRET_UNAVAILABLE", "Worker secret unavailable")
		}
		values[item.Version.Name] = value
		versions[item.Version.Name] = item.Version.ID
	}
	if workerapp.WorkerSecretsDigest(versions) != digest {
		return Secrets{}, domain.Problem(503, "WORKER_SECRET_UNAVAILABLE", "Worker secret binding mismatch")
	}
	return Secrets{Version: version, Digest: digest, Values: values}, nil
}
func (s *Service) Ack(ctx context.Context, pool string, in domain.WorkerAck) error {
	if in.Pool != pool || in.Pod == "" || in.UID == "" {
		return domain.Problem(422, "ACK_INVALID", "Pod UID and pool required")
	}
	head, err := s.Head(ctx)
	if err != nil {
		return err
	}
	if in.Revision < 0 || int64(in.Revision) > head {
		return domain.Problem(422, "ACK_INVALID", "Config revision is outside the published range")
	}
	in.Time = time.Now().UTC()
	return s.Store.RecordAck(ctx, in)
}

type Assets struct {
	Version string                              `json:"version_id"`
	Config  *domain.WorkerAssetsConfig          `json:"config"`
	Files   []workerartifact.WorkerManifestFile `json:"files"`
}

func (s *Service) Assets(ctx context.Context, version, pool string) (Assets, error) {
	assets, err := s.Controller.VersionAssets(ctx, version, pool)
	if err != nil {
		return Assets{}, err
	}
	return Assets{Version: assets.Version.ID, Config: assets.Config, Files: assets.Files}, nil
}

type AssetURL struct {
	Path        string `json:"path"`
	Digest      string `json:"sha256"`
	Size        int    `json:"size_bytes"`
	ContentType string `json:"content_type"`
	DownloadURL string `json:"download_url"`
}

func (s *Service) AssetURL(ctx context.Context, version, name string, pool appconfig.WorkerRuntimePool) (AssetURL, error) {
	assets, err := s.Controller.VersionAssets(ctx, version, pool.ID)
	if err != nil {
		return AssetURL{}, err
	}
	var file *workerartifact.WorkerManifestFile
	for i := range assets.Files {
		if assets.Files[i].Path == name {
			file = &assets.Files[i]
			break
		}
	}
	if file == nil {
		return AssetURL{}, domain.NotFound()
	}
	prefix := "int"
	if appconfig.RuntimePoolHasAccess(pool, "public") {
		prefix = "pub"
	}
	key := workerapp.WorkerAssetObjectKey(prefix, assets.Version.Organization, assets.Version.Service, assets.Artifact.ArchiveDigest, name)
	url, err := s.Controller.S3Runtime.PresignedGetObject(ctx, s.Config.S3Bucket, key, 15*time.Minute, nil)
	if err != nil {
		return AssetURL{}, domain.Problem(503, "ASSET_UNAVAILABLE", "Asset storage unavailable")
	}
	return AssetURL{Path: file.Path, Digest: file.Digest, Size: file.Size, ContentType: file.ContentType, DownloadURL: url.String()}, nil
}
func (s *Service) Route(ctx context.Context, host, pool string) (workerapp.WorkerConfigSnapshot, error) {
	host = strings.ToLower(host)
	if len(host) > 253 || strings.ContainsAny(host, "/\\: ") {
		return workerapp.WorkerConfigSnapshot{}, domain.NotFound()
	}
	projection, revision, err := s.Store.RuntimeConfigRoute(ctx, host, pool)
	if err != nil {
		return workerapp.WorkerConfigSnapshot{}, err
	}
	return workerapp.WorkerConfigSnapshot{Pool: pool, ConfigRevision: revision, Routes: projection.Routes, Versions: projection.Versions}, nil
}

// StaticAssetURL authorizes an immutable asset in this runtime pool. Private
// external buckets never need anonymous access or a bucket-wide policy rewrite.
func (s *Service) StaticAssetURL(
	ctx context.Context,
	organization, service, digest, name, prefix string,
	pool appconfig.WorkerRuntimePool,
) (AssetURL, error) {
	invalidPrefix := prefix != "pub" && prefix != "int"
	invalidDigest := len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != ""
	invalidOwner := organization == "" || service == "" ||
		strings.ContainsAny(organization+service, "/\\")
	if invalidPrefix || invalidDigest || invalidOwner {
		return AssetURL{}, domain.NotFound()
	}
	if _, ok := workerartifact.WorkerAssetPath(name); !ok {
		return AssetURL{}, domain.NotFound()
	}
	access := "private"
	if prefix == "pub" {
		access = "public"
	}
	if !appconfig.RuntimePoolHasAccess(pool, access) {
		return AssetURL{}, domain.Forbidden()
	}
	file, err := s.Controller.StaticAsset(ctx, prefix, organization, service, digest, name)
	if err != nil {
		return AssetURL{}, err
	}
	key := workerapp.WorkerAssetObjectKey(prefix, organization, service, digest, name)
	signed, err := s.Controller.S3Runtime.PresignedGetObject(ctx, s.Config.S3Bucket, key, 15*time.Minute, nil)
	if err != nil {
		return AssetURL{}, domain.Problem(503, "ASSET_UNAVAILABLE", "Asset storage unavailable")
	}
	return AssetURL{
		Path:        name,
		Digest:      file.Digest,
		Size:        file.Size,
		ContentType: file.ContentType,
		DownloadURL: signed.String(),
	}, nil
}
