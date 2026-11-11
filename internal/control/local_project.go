package control

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/artifact"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

// Allow for multipart headers in addition to the bounded ZIP upload.
const localProjectRequestMax = 34 << 20

func localProjectUploadDeadline(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response := http.NewResponseController(w)
		now := time.Now()
		_ = response.SetReadDeadline(now.Add(2 * time.Minute))
		_ = response.SetWriteDeadline(now.Add(2*time.Minute + 30*time.Second))
		next(w, r)
	}
}

func (s *Server) workerLocalBuildCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		return nil, 0, problem(422, "IDEMPOTENCY_REQUIRED", "A valid Idempotency-Key header is required")
	}
	service := s.workerApplication(r)
	if service.Controller == nil {
		return nil, 0, problem(503, "WORKERS_NOT_CONFIGURED", "Worker uploads unavailable")
	}
	ref := workerRef(r)
	preflight := s.workerApplication(nil)
	if err := preflight.CheckLocalProjectTarget(st, p, ref); err != nil {
		return nil, 0, err
	}
	return deferredResponse{run: func(ctx context.Context) (any, int, error) {
		archive, files, err := readLocalProjectUpload(r)
		if err != nil {
			return nil, 0, err
		}
		project, err := artifact.PrepareLocalProject(archive, files, r.URL.Query().Get("project_root"))
		if err != nil {
			return nil, 0, err
		}
		bundle, err := workerapp.WorkerSourceBundle(project.Files, nil)
		if err != nil {
			return nil, 0, problem(422, "LOCAL_PROJECT_INVALID", "Project paths or size exceed source archive limits")
		}
		in := workerapp.LocalBuildSource{
			Key: key, Digest: artifact.BytesDigest(bundle),
			ConfigPath: project.ConfigPath, Entrypoint: project.Entrypoint,
			CompatibilityDate: project.CompatibilityDate, CompatibilityFlags: project.CompatibilityFlags,
		}
		credentials, err := s.readCredentials(r)
		if err != nil {
			return nil, 0, err
		}
		var build domain.WorkerBuild
		err = s.Store.View(ctx, PartitionAll, func(state *State) error {
			actor, err := s.protectedPrincipal(state, credentials, r, false)
			if err != nil {
				return err
			}
			build, err = preflight.FindLocalProjectBuild(state, actor, ref, in)
			return err
		})
		if err != nil {
			return nil, 0, err
		}
		if build.ID != "" {
			return map[string]any{"kind": project.Kind, "build": build}, http.StatusAccepted, nil
		}
		in.SourceKey, err = service.Controller.StageLocalProject(ctx, ref.Organization, bundle)
		if err != nil {
			return nil, 0, problem(503, "SOURCE_UPLOAD_UNAVAILABLE", "Source storage unavailable")
		}
		credentials, err = s.readCredentials(r)
		if err != nil {
			return nil, 0, err
		}
		err = s.Store.Mutate(ctx, PartitionAll, PartitionWorkers, func(state *State) error {
			actor, err := s.protectedPrincipal(state, credentials, r, false)
			if err != nil {
				return err
			}
			build, err = service.QueueLocalProject(ctx, state, actor, ref, in)
			return err
		})
		if err != nil {
			return nil, 0, err
		}
		return map[string]any{"kind": project.Kind, "build": build}, http.StatusAccepted, nil
	}}, http.StatusAccepted, nil
}

func readLocalProjectUpload(r *http.Request) ([]byte, map[string][]byte, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nil, problem(400, "LOCAL_PROJECT_INVALID", "Expected a multipart project upload")
	}
	files := map[string][]byte{}
	var archive []byte
	var total int
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			if archive != nil {
				return archive, nil, nil
			}
			return nil, files, nil
		}
		if err != nil {
			return nil, nil, problem(400, "LOCAL_PROJECT_INVALID", "Invalid multipart project upload")
		}
		name := part.FormName()
		if name == "archive" && archive == nil && len(files) == 0 && part.FileName() != "" {
			archive, err = io.ReadAll(io.LimitReader(part, artifact.LocalProjectUploadMax+1))
			if err != nil || len(archive) == 0 || len(archive) > artifact.LocalProjectUploadMax {
				return nil, nil, problem(422, "LOCAL_PROJECT_INVALID", "ZIP archive must be non-empty and at most 32 MiB")
			}
			continue
		}
		path, file := strings.CutPrefix(name, "file:")
		if !file || archive != nil || part.FileName() == "" || path == "" {
			return nil, nil, problem(422, "LOCAL_PROJECT_INVALID", "Upload either one ZIP or a directory of files")
		}
		if _, exists := files[path]; exists || len(files) >= artifact.WorkerRepositoryProjectFilesMax {
			return nil, nil, problem(422, "LOCAL_PROJECT_INVALID", "Duplicate or excess project files")
		}
		data, err := io.ReadAll(io.LimitReader(part, artifact.WorkerRepositoryProjectFileMax+1))
		total += len(data)
		if err != nil || len(data) > artifact.WorkerRepositoryProjectFileMax || total > artifact.WorkerRepositoryProjectBytesMax {
			return nil, nil, problem(422, "LOCAL_PROJECT_INVALID", "Project exceeds file size limits")
		}
		files[path] = data
	}
}
