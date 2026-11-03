package control

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	artifact "github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/configfile"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func (s *Server) cfAssetSession(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var in struct {
		Manifest map[string]workerapp.AssetUploadFile `json:"manifest"`
	}
	if err := cfBody(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if _, err := s.cfEnsureScript(r, r.PathValue("script")); err != nil {
		s.writeError(w, r, err)
		return
	}
	var err error
	r, err = s.cfRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err = s.cfCall(r, false, func(st *State, p Principal) error {
		_, e := s.workerTarget(st, r, r.PathValue("environment"), "service.publish", p)
		return e
	}); err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.workers == nil {
		s.writeError(w, r, problem(503, "WORKERS_NOT_CONFIGURED", "Worker uploads unavailable"))
		return
	}
	out, err := s.workers.StartAssetUpload(r.Context(), workerRef(r), in.Manifest)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, cfResult(out))
}

// Bulk uploads are authorized by the short-lived manifest capability, rather
// than by the user's API key. The token cannot access another account or file.
func (s *Server) cfAssetUpload(w http.ResponseWriter, r *http.Request) {
	if err := s.checkRateLimit(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || token == r.Header.Get("Authorization") {
		s.writeError(w, r, problem(401, "UNAUTHENTICATED", "Asset upload token required"))
		return
	}
	a, err := s.Store.WorkerAssetSession(r.Context(), r.PathValue("account"), securetoken.Digest(token), false)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.workers == nil {
		s.writeError(w, r, problem(503, "WORKERS_NOT_CONFIGURED", "Worker uploads unavailable"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 30<<20)
	if hash := r.PathValue("hash"); hash != "" {
		raw, e := io.ReadAll(io.LimitReader(r.Body, artifact.WorkerAssetMax+1))
		err = e
		if err == nil {
			err = s.workers.UploadAssetFile(r.Context(), a, hash, raw)
		}
	} else {
		reader, e := r.MultipartReader()
		if e != nil {
			s.writeError(w, r, problem(400, "INVALID_REQUEST", "Expected multipart asset upload"))
			return
		}
		seen := map[string]bool{}
		for count := 0; ; count++ {
			part, e := reader.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				err = problem(400, "INVALID_REQUEST", "Invalid asset upload")
				break
			}
			hash := part.FormName()
			if seen[hash] || count >= artifact.WorkerAssetsFilesMax {
				err = problem(422, "WORKER_ASSETS_INVALID", "Duplicate or excess asset parts")
				break
			}
			seen[hash] = true
			raw, e := io.ReadAll(io.LimitReader(part, ((artifact.WorkerAssetMax+2)/3)*4+1))
			if e != nil {
				err = e
				break
			}
			if r.URL.Query().Get("base64") == "true" {
				raw, e = base64.StdEncoding.Strict().DecodeString(string(raw))
				if e != nil {
					err = problem(422, "WORKER_ASSETS_INVALID", "Invalid base64 asset")
					break
				}
			}
			if err = s.workers.UploadAssetFile(r.Context(), a, hash, raw); err != nil {
				break
			}
		}
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	completion, err := s.workers.CompleteAssetUpload(r.Context(), a.Organization, token)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.json(w, 200, cfResult(map[string]string{"jwt": completion}))
}

func (s *Server) cfUploadedAssets(r *http.Request, meta cfUploadMetadata, in workerapp.BindingsInput) (*artifact.WorkerManifestAssets, map[string][]byte, error) {
	config := domain.WorkerAssetsConfig{Directory: "public", HTMLHandling: "auto-trailing-slash", NotFoundHandling: "none", RunWorkerFirst: []string{}}
	if in.AssetsBinding != nil {
		config.Binding = *in.AssetsBinding
	}
	var upload struct {
		JWT    string `json:"jwt"`
		Config struct {
			HTML      string          `json:"html_handling"`
			NotFound  string          `json:"not_found_handling"`
			First     json.RawMessage `json:"run_worker_first"`
			BasePath  string          `json:"base_path"`
			Headers   string          `json:"_headers"`
			Redirects string          `json:"_redirects"`
		} `json:"config"`
	}
	hasConfig := len(meta.Assets) > 0 && string(meta.Assets) != "null"
	if hasConfig {
		if err := configfile.DecodeJSON(meta.Assets, &upload); err != nil {
			return nil, nil, problem(422, "UNSUPPORTED_ASSETS", "Invalid or unsupported assets configuration")
		}
		if upload.Config.BasePath != "" && upload.Config.BasePath != "/" || upload.Config.Headers != "" || upload.Config.Redirects != "" {
			return nil, nil, problem(422, "UNSUPPORTED_ASSETS", "Asset base_path, _headers and _redirects are not supported")
		}
		if upload.Config.HTML != "" {
			config.HTMLHandling = upload.Config.HTML
		}
		if upload.Config.NotFound != "" {
			config.NotFoundHandling = upload.Config.NotFound
		}
		first := upload.Config.First
		if string(first) == "true" {
			config.RunWorkerFirst = []string{"/*"}
		} else if len(first) > 0 && string(first) != "false" && string(first) != "null" {
			if err := json.Unmarshal(first, &config.RunWorkerFirst); err != nil {
				return nil, nil, problem(422, "WORKER_ASSETS_INVALID", "Invalid run_worker_first")
			}
		}
	}
	if err := artifact.ValidateWorkerAssets(&config); err != nil {
		return nil, nil, err
	}
	if upload.JWT != "" {
		return s.workers.UploadedAssets(r.Context(), workerRef(r), upload.JWT, config)
	}
	if meta.KeepAssets {
		var previous WorkerVersion
		var art domain.WorkerArtifact
		err := s.cfCall(r, false, func(st *State, p Principal) error {
			_, env, e := s.cfCurrent(st, p, r)
			if e != nil {
				return e
			}
			previous = st.Workers.Versions[env.Version]
			art = st.Workers.Artifacts[previous.Artifact]
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		if art.ID == "" {
			return nil, nil, problem(409, "ASSETS_NOT_READY", "There are no deployed assets to retain")
		}
		code, err := s.workers.ArtifactCode(r.Context(), art)
		if err != nil {
			return nil, nil, err
		}
		if code.Assets == nil {
			return nil, nil, problem(409, "ASSETS_NOT_READY", "There are no deployed assets to retain")
		}
		if !hasConfig {
			config = *code.Assets
		}
		if in.AssetsBinding != nil && *in.AssetsBinding != "" {
			config.Binding = *in.AssetsBinding
		}
		// Preserve the binding declaration alongside the retained immutable files.
		if in.AssetsBinding != nil {
			*in.AssetsBinding = config.Binding
		}
		manifest := &artifact.WorkerManifestAssets{Directory: config.Directory, Binding: config.Binding, HTMLHandling: config.HTMLHandling, NotFoundHandling: config.NotFoundHandling, RunWorkerFirst: config.RunWorkerFirst, Files: []artifact.WorkerManifestFile{}}
		names := make([]string, 0, len(code.AssetFiles))
		for name := range code.AssetFiles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			f := code.AssetFiles[name]
			manifest.Files = append(manifest.Files, artifact.WorkerManifestFile{Path: name, Digest: f.Digest, Size: f.Size, ContentType: f.ContentType})
		}
		return manifest, code.AssetBytes, nil
	}
	if hasConfig || config.Binding != "" {
		return nil, nil, problem(422, "WORKER_ASSETS_INVALID", "A completed assets upload is required")
	}
	return nil, nil, nil
}
