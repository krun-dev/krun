package control

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestLocalUploadMultipartPreservesProjectPathsAndRejectsDuplicates(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		count := 1
		if duplicate {
			count++
		}
		for range count {
			part, err := form.CreateFormFile("file:assets/logo.svg", "logo.svg")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = part.Write([]byte("<svg/>")); err != nil {
				t.Fatal(err)
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		archive, files, err := readLocalProjectUpload(req)
		if duplicate {
			if err == nil {
				t.Fatal("duplicate path was accepted")
			}
			continue
		}
		if err != nil || archive != nil || len(files) != 1 || string(files["assets/logo.svg"]) != "<svg/>" {
			t.Fatalf("multipart changed project paths: %v, %v, %v", archive, files, err)
		}
	}
}

func TestLocalUploadMultipartSelectsOnlyArchive(t *testing.T) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("archive", "project.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte("zip bytes")); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	archive, files, err := readLocalProjectUpload(req)
	if err != nil || files != nil || string(archive) != "zip bytes" {
		t.Fatalf("expected only the archive input, got %q, %v, %v", archive, files, err)
	}
}

func TestPostgresLocalUploadQueuesStaticAndWorkerProjects(t *testing.T) {
	s, store := testServer(t)
	ctx := context.Background()
	s.Config.Workers = &appconfig.WorkersConfig{S3Bucket: "test", S3BucketMode: "external"}
	staged := map[string][]byte{}
	s.workers = &workerapp.Controller{
		Config: s.Config.Workers, Service: &workerapp.Service{Store: store},
		PutObject: func(_ context.Context, _, key string, input io.Reader, _ int64, _ objectstore.PutObjectOptions) (objectstore.UploadInfo, error) {
			raw, err := io.ReadAll(input)
			staged[key] = raw
			return objectstore.UploadInfo{}, err
		},
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Workers["svc-a"] = domain.Worker{Environments: map[string]domain.WorkerEnvironment{
			"production": {Name: "production", Enabled: true},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/orgs/org-a/workers/svc-a/environments/production/local-builds"
	submit := func(token, key string, files map[string]string, asZIP bool) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		writePart := func(field, filename string, content []byte) {
			t.Helper()
			part, err := form.CreateFormFile(field, filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = part.Write(content); err != nil {
				t.Fatal(err)
			}
		}
		if asZIP {
			var buf bytes.Buffer
			archive := zip.NewWriter(&buf)
			for name, content := range files {
				file, err := archive.Create("app/" + name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = io.WriteString(file, content); err != nil {
					t.Fatal(err)
				}
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			writePart("archive", "project.zip", buf.Bytes())
		} else {
			for name, content := range files {
				writePart("file:"+name, name, []byte(content))
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", path, &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		resp := httptest.NewRecorder()
		s.ServeHTTP(resp, req)
		return resp
	}
	readBuild := func(resp *httptest.ResponseRecorder, kind, configPath string) string {
		t.Helper()
		if resp.Code != 202 {
			t.Fatalf("upload rejected: %d %s", resp.Code, resp.Body)
		}
		var out struct {
			Kind  string
			Build domain.WorkerBuild
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Kind != kind || out.Build.ID == "" || out.Build.State != "queued" || out.Build.Repository != nil {
			t.Fatalf("unexpected upload response: %+v", out)
		}
		private, found, err := store.WorkerBuildPrivate(ctx, out.Build.ID)
		if err != nil || !found || private.ConfigPath != configPath || len(staged[private.SourceKey]) == 0 {
			t.Fatalf("project was not persisted for the builder: %+v, %v, %v", private, found, err)
		}
		return out.Build.ID
	}

	static := map[string]string{"index.html": "<h1>local upload</h1>"}
	if resp := submit("krun_cli_viewer", "no-access", static, false); resp.Code != 403 || len(staged) != 0 {
		t.Fatalf("unauthorized upload: %d, %d staged objects, %s", resp.Code, len(staged), resp.Body)
	}
	id := readBuild(submit("krun_cli_admin", "static", static, false), "static", "wrangler.jsonc")
	if again := readBuild(submit("krun_cli_admin", "static", static, false), "static", "wrangler.jsonc"); id != again {
		t.Fatalf("retry queued two builds: %s, %s", id, again)
	}
	if len(staged) != 1 {
		t.Fatalf("replay staged a second source object: %d", len(staged))
	}
	static["index.html"] = "<h1>changed</h1>"
	if resp := submit("krun_cli_admin", "static", static, false); resp.Code != 409 {
		t.Fatalf("changed upload did not conflict: %d %s", resp.Code, resp.Body)
	}
	if len(staged) != 1 {
		t.Fatalf("conflicting retry staged another source object: %d", len(staged))
	}
	worker := map[string]string{
		"wrangler.toml": "name = 'example'\nmain = 'src/index.js'\n",
		"src/index.js":  "export default {fetch(){return new Response('ok')}}",
	}
	readBuild(submit("krun_cli_admin", "worker", worker, true), "worker", "wrangler.toml")
}
