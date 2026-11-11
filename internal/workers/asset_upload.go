package workers

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	artifact "github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/zeebo/blake3"
)

type AssetUploadFile struct {
	Hash string `json:"hash"`
	Size int    `json:"size"`
}
type AssetUploadSession struct {
	Token   string     `json:"jwt"`
	Buckets [][]string `json:"buckets"`
}

var assetHashPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// The client reads exp from the token envelope. Authentication always compares
// the hash of the entire opaque capability with the stored session; decoded
// claims never grant authority.
func assetUploadToken(expires time.Time, nonce string) string {
	payload, _ := json.Marshal(map[string]any{"exp": expires.Unix()})
	return base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + nonce
}
func assetCompletionToken(token string, expires time.Time) string {
	return assetUploadToken(expires, securetoken.Digest("assets-complete/"+token))
}

func (w *Controller) StartAssetUpload(ctx context.Context, ref WorkerRef, manifest map[string]AssetUploadFile) (AssetUploadSession, error) {
	if len(manifest) > artifact.WorkerAssetsFilesMax {
		return AssetUploadSession{}, domain.Problem(413, "WORKER_ASSETS_INVALID", "Too many asset files")
	}
	at := time.Now().UTC()
	a := domain.WorkerAssetSession{ID: securetoken.UUID(), Organization: ref.Organization, Worker: ref.Worker, Created: at, Expires: at.Add(time.Hour)}
	token := assetUploadToken(a.Expires, securetoken.Random())
	a.UploadHash = securetoken.Digest(token)
	a.CompletionHash = securetoken.Digest(assetCompletionToken(token, a.Expires))
	names := make([]string, 0, len(manifest))
	for name := range manifest {
		names = append(names, name)
	}
	sort.Strings(names)
	total := 0
	hashes := []string{}
	seen := map[string]bool{}
	for _, name := range names {
		f := manifest[name]
		clean, valid := artifact.WorkerAssetPath(strings.TrimPrefix(name, "/"))
		if !strings.HasPrefix(name, "/") || !valid || !assetHashPattern.MatchString(f.Hash) || f.Size < 0 || f.Size > artifact.WorkerAssetMax {
			return AssetUploadSession{}, domain.Invalid("WORKER_ASSETS_INVALID", "Invalid asset path, hash or size")
		}
		total += f.Size
		if total > artifact.WorkerAssetsTotalMax {
			return AssetUploadSession{}, domain.Problem(413, "WORKER_ASSETS_INVALID", "Assets exceed size limit")
		}
		kind, _ := artifact.WorkerAssetType(clean)
		a.Files = append(a.Files, domain.WorkerAssetSessionFile{Path: clean, Hash: f.Hash, Size: f.Size, ObjectKey: "uploads/" + ref.Organization + "/cf-assets/" + a.ID + "/" + f.Hash, ContentType: kind})
		if !seen[f.Hash] {
			hashes = append(hashes, f.Hash)
			seen[f.Hash] = true
		}
	}
	if err := w.Service.Store.InsertWorkerAssetSession(ctx, a); err != nil {
		return AssetUploadSession{}, err
	}
	// A single bounded bucket fits the platform's existing total-asset limit.
	buckets := [][]string{}
	if len(hashes) > 0 {
		buckets = append(buckets, hashes)
	} else {
		token = assetCompletionToken(token, a.Expires)
	}
	return AssetUploadSession{Token: token, Buckets: buckets}, nil
}

func AssetUploadHash(name string, raw []byte) string {
	extension := path.Ext(name)
	// Node's path.extname treats a single leading dot as part of the basename.
	if extension == path.Base(name) {
		extension = ""
	}
	sum := blake3.Sum256([]byte(base64.StdEncoding.EncodeToString(raw) + strings.TrimPrefix(extension, ".")))
	return hex.EncodeToString(sum[:16])
}

func (w *Controller) UploadAssetFile(ctx context.Context, a domain.WorkerAssetSession, hash string, raw []byte) error {
	var selected *domain.WorkerAssetSessionFile
	for _, f := range a.Files {
		if f.Hash == hash {
			if len(raw) != f.Size || AssetUploadHash(f.Path, raw) != f.Hash {
				return domain.Invalid("WORKER_ASSETS_INVALID", "Asset size or hash does not match manifest")
			}
			item := f
			selected = &item
		}
	}
	if selected == nil {
		return domain.Invalid("WORKER_ASSETS_INVALID", "Asset was not declared in this upload")
	}
	if err := w.EnsureBucket(ctx); err != nil {
		return err
	}
	if err := w.PutBytes(ctx, selected.ObjectKey, raw, objectstore.PutObjectOptions{ContentType: selected.ContentType}); err != nil {
		return err
	}
	return w.Service.Store.MarkWorkerAssetUploaded(ctx, a.ID, hash, selected.ContentType)
}

func (w *Controller) CompleteAssetUpload(ctx context.Context, org, token string) (string, error) {
	a, err := w.Service.Store.WorkerAssetSession(ctx, org, securetoken.Digest(token), false)
	if err != nil {
		return "", err
	}
	for _, f := range a.Files {
		if !f.Uploaded {
			return "", nil
		}
	}
	return assetCompletionToken(token, a.Expires), nil
}

// UploadedAssets resolves only a completed capability for this exact Worker.
// The resulting bytes become part of its ordinary immutable artifact and use
// the same validation, publication, rollback and GC paths as native assets.
func (w *Controller) UploadedAssets(ctx context.Context, ref WorkerRef, token string, config domain.WorkerAssetsConfig) (*artifact.WorkerManifestAssets, map[string][]byte, error) {
	a, err := w.Service.Store.WorkerAssetSession(ctx, ref.Organization, securetoken.Digest(token), true)
	if err != nil {
		return nil, nil, err
	}
	if a.Worker != ref.Worker {
		return nil, nil, domain.Forbidden()
	}
	files := map[string][]byte{}
	manifest := &artifact.WorkerManifestAssets{Directory: "public", BasePath: config.BasePath, Headers: config.Headers, Redirects: config.Redirects, Binding: config.Binding, RunWorkerFirst: config.RunWorkerFirst, HTMLHandling: config.HTMLHandling, NotFoundHandling: config.NotFoundHandling, Files: []artifact.WorkerManifestFile{}}
	for _, f := range a.Files {
		if !f.Uploaded {
			return nil, nil, domain.Conflict("ASSETS_NOT_READY", "Asset upload is incomplete")
		}
		raw, e := w.GetObject(ctx, f.ObjectKey)
		if e != nil {
			return nil, nil, e
		}
		if len(raw) != f.Size || AssetUploadHash(f.Path, raw) != f.Hash {
			return nil, nil, domain.Invalid("WORKER_ASSETS_INVALID", "Uploaded asset integrity check failed")
		}
		kind, _ := artifact.WorkerAssetType(f.Path)
		files[f.Path] = raw
		manifest.Files = append(manifest.Files, artifact.WorkerManifestFile{Path: f.Path, Digest: artifact.BytesDigest(raw), Size: len(raw), ContentType: kind})
	}
	return manifest, files, nil
}
