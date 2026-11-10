package control

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestCloudflareJSONAndMetadataBindings(t *testing.T) {
	server, store, _ := cfTestServer(t)
	var meta cfUploadMetadata
	if err := json.Unmarshal([]byte(`{"bindings":[{"name":"CONFIG","type":"json","json":{"nested":[1,true,null,{"x":"y"}]}},{"name":"META","type":"version_metadata"}],"annotations":{"workers/tag":"v1"}}`), &meta); err != nil {
		t.Fatal(err)
	}
	in, err := cfBindingInput(meta, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.View(context.Background(), PartitionAll, func(st *State) error {
		p := Principal{User: st.Users["admin"], Kind: "cli_session"}
		// Isolate snapshot and binding validation from the Cedar policy tests.
		p.User = domain.User{ID: "admin", Source: "local", Status: "active"}
		app := server.workerApplication(nil)
		app.Require = func(*domain.State, Principal, string, Target) error { return nil }
		ref := workerapp.WorkerRef{Organization: "org-a", Worker: "svc-a", Environment: "production"}
		if e := app.StageBindings(st, p, ref, in); e != nil {
			return e
		}
		env := st.Workers.Workers["svc-a"].Environments["production"]
		version := workerapp.SnapshotVersion("org-a", "svc-a", "production", WorkerArtifact{}, env)
		original := version.Variables["CONFIG"].(map[string]any)["nested"].([]any)[3].(map[string]any)
		env.Variables["CONFIG"].(map[string]any)["nested"].([]any)[3].(map[string]any)["x"] = "changed"
		if original["x"] != "y" || version.VersionMetadata != "META" {
			t.Fatal("version bindings were not frozen")
		}
		bindings := cfBindings(WorkerEnvironment{Variables: version.Variables, VersionMetadata: version.VersionMetadata})
		if bindings[0].(map[string]any)["type"] != "json" || bindings[1].(map[string]any)["type"] != "version_metadata" {
			t.Fatal(bindings)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, bindings := range [][]cfBinding{
		{{Name: "X", Type: "json"}},
		{{Name: "X", Type: "json", JSON: json.RawMessage(`{}`)}, {Name: "X", Type: "version_metadata"}},
		{{Name: "M1", Type: "version_metadata"}, {Name: "M2", Type: "version_metadata"}},
	} {
		if _, e := cfBindingInput(cfUploadMetadata{Bindings: bindings}, false); e == nil {
			t.Fatal("invalid bindings accepted", bindings)
		}
	}
}

func TestAssetSessionCapabilitiesAndValidation(t *testing.T) {
	_, store, _ := cfTestServer(t)
	controller := &workerapp.Controller{Service: &workerapp.Service{Store: store}}
	ref := workerapp.WorkerRef{Organization: "org-a", Worker: "svc-a"}
	data := []byte("asset data")
	if workerapp.AssetUploadHash(".nojekyll", data) != workerapp.AssetUploadHash("extensionless", data) {
		t.Fatal("dotfile hashing differs from Wrangler path.extname")
	}
	hash := workerapp.AssetUploadHash("index.html", data)
	started, err := controller.StartAssetUpload(context.Background(), ref, map[string]workerapp.AssetUploadFile{"/index.html": {Hash: hash, Size: len(data)}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.WorkerAssetSession(context.Background(), ref.Organization, securetoken.Digest(started.Token), false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(started.Buckets, [][]string{{hash}}) {
		t.Fatal(started.Buckets)
	}
	if _, err = store.WorkerAssetSession(context.Background(), "org-b", securetoken.Digest(started.Token), false); err == nil {
		t.Fatal("cross-account token accepted")
	}
	if _, err = store.WorkerAssetSession(context.Background(), ref.Organization, securetoken.Digest(started.Token+"forged"), false); err == nil {
		t.Fatal("forged token accepted")
	}
	if token, e := controller.CompleteAssetUpload(context.Background(), ref.Organization, started.Token); e != nil || token != "" {
		t.Fatal("incomplete upload completed", e)
	}
	if err = controller.UploadAssetFile(context.Background(), a, hash, []byte("wrong")); err == nil {
		t.Fatal("corrupt asset accepted")
	}
	if err = store.MarkWorkerAssetUploaded(context.Background(), a.ID, hash, "text/html"); err != nil {
		t.Fatal(err)
	}
	complete, err := controller.CompleteAssetUpload(context.Background(), ref.Organization, started.Token)
	if err != nil || complete == "" {
		t.Fatal("completion token missing", err)
	}
	if _, err = store.WorkerAssetSession(context.Background(), ref.Organization, securetoken.Digest(complete), false); err == nil {
		t.Fatal("completion token can upload")
	}
	if _, err = store.WorkerAssetSession(context.Background(), ref.Organization, securetoken.Digest(complete), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = controller.UploadedAssets(context.Background(), workerapp.WorkerRef{Organization: "org-a", Worker: "other"}, complete, domain.WorkerAssetsConfig{}); err == nil {
		t.Fatal("cross-Worker completion accepted")
	}
	for _, name := range []string{"relative.html", "/../escape.html", "//double.html", "/bad\\path.html"} {
		if _, err = controller.StartAssetUpload(context.Background(), ref, map[string]workerapp.AssetUploadFile{name: {Hash: hash, Size: len(data)}}); err == nil {
			t.Fatal("unsafe asset path", name)
		}
	}
	expired := domain.WorkerAssetSession{ID: "expired", Organization: ref.Organization, UploadHash: securetoken.Digest("expired-token"), CompletionHash: securetoken.Digest("expired-completion"), Created: time.Now().Add(-2 * time.Hour), Expires: time.Now().Add(-time.Hour)}
	if err = store.InsertWorkerAssetSession(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	if _, err = store.WorkerAssetSession(context.Background(), ref.Organization, expired.UploadHash, false); err == nil {
		t.Fatal("expired upload accepted")
	}
}

func TestCloudflareKeepVariableTypes(t *testing.T) {
	for _, test := range []struct {
		name string
		keep []string
		want map[string]any
	}{
		{"replace", nil, map[string]any{"CHANGED": "new"}},
		{"text", []string{"plain_text"}, map[string]any{"CHANGED": "new", "TEXT": "old"}},
		{"json", []string{"json"}, map[string]any{"CHANGED": "new", "JSON": map[string]any{"enabled": true}, "NULL": nil}},
		{"keep-vars", []string{"plain_text", "json"}, map[string]any{"CHANGED": "new", "TEXT": "old", "JSON": map[string]any{"enabled": true}, "NULL": nil}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, store, _ := cfTestServer(t)
			input, err := cfBindingInput(cfUploadMetadata{Keep: test.keep, Bindings: []cfBinding{{Name: "CHANGED", Type: "plain_text", Text: "new"}}}, false)
			if err != nil {
				t.Fatal(err)
			}
			err = store.View(context.Background(), PartitionAll, func(st *State) error {
				service := st.Workers.Workers["svc-a"]
				env := service.Environments["production"]
				env.Variables = map[string]any{"TEXT": "old", "CHANGED": "old", "JSON": map[string]any{"enabled": true}, "NULL": nil}
				service.Environments["production"] = env
				st.Workers.Workers["svc-a"] = service
				app := server.workerApplication(nil)
				app.Require = func(*domain.State, Principal, string, Target) error { return nil }
				err := app.StageBindings(st, Principal{User: domain.User{ID: "admin", Source: "local", Status: "active"}, Kind: "cli_session"}, workerapp.WorkerRef{Organization: "org-a", Worker: "svc-a", Environment: "production"}, input)
				if err != nil {
					return err
				}
				actual := st.Workers.Workers["svc-a"].Environments["production"].Variables
				if !reflect.DeepEqual(map[string]any(actual), test.want) {
					t.Fatalf("got %#v, want %#v", actual, test.want)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
