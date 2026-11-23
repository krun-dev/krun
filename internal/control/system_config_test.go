package control

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/krun-dev/krun/internal/cli"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func TestSystemConfigAuthorizationAndRedaction(t *testing.T) {
	s, store := testServer(t)
	const testKey = "krun_key_config_test"
	if err := store.Update(t.Context(), PartitionIAM, func(st *State) error {
		assignment := st.Assignments["viewer"]
		assignment.Role = "org_admin"
		st.Assignments[assignment.ID] = assignment
		st.Keys["key-config"] = domain.APIKey{
			ID: "key-config", ScopeKind: "system", Owner: "admin",
			Policy: domain.Policy{Version: 1}, Revision: 1,
		}
		st.KeyHashes["key-config"] = digest(testKey)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	const path = "/api/v1/system/config"
	for token, want := range map[string]int{"": 401, "krun_cli_viewer": 403, testKey: 403} {
		if w := request(s, http.MethodGet, path, token, ""); w.Code != want {
			t.Fatalf("got status %d, want %d: %s", w.Code, want, w.Body.String())
		}
	}

	adminConfig := func(expected string) {
		t.Helper()
		w := request(s, http.MethodGet, path, "krun_cli_admin", "")
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected config response: %d, %s", w.Code, w.Body.String())
		}
		var got, want any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(expected), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("response did not match the approved configuration fields and values")
		}
	}
	adminConfig(`{"workers":null}`)

	const private = "sensitive-config-fixture"
	s.Config.DB = private
	s.Config.SMTP = appconfig.SMTPConfig{Password: private, Host: private}
	s.Config.Deployment = &appconfig.DeploymentConfig{
		RuntimeReplicas: 3, ValidatorReplicas: 2,
		DNSResolver: private, RuntimeImage: private, ImagePullSecrets: []string{private},
	}
	s.Config.Workers = &appconfig.WorkersConfig{
		RuntimeAPIURL: private, BuilderMode: "kubernetes", PreviewLimit: 32, ValidatorPool: "private-b",
		KubernetesAPI: private, KubernetesTokenFile: private, ArtifactStorage: appconfig.WorkerArtifactStorageConfig{
			Credentials: appconfig.ArtifactStorageCredentials{
				AccessKeyID:     private,
				SecretAccessKey: private,
			},
		},
		SecretsKeys: map[string]string{"active": private},
		RuntimePools: []appconfig.WorkerRuntimePool{
			{
				ID: "public-a", Access: []string{"public"}, Default: true,
				PublicEgress: "direct", PrivateEgress: "deny",
				RuntimeService: private, RuntimeToken: private, ManagementToken: private,
				Labels: map[string]string{"zone": private},
			},
			{
				ID: "private-b", Access: []string{"private"},
				PublicEgress: "deny", PrivateEgress: "direct",
				ValidatorService: private, ValidatorToken: private, ValidatorManagementBase: private,
			},
		},
		NetworkProfiles: []appconfig.WorkerNetworkProfile{{
			ID: "standard", Name: private, Description: private, PoolSelector: map[string]string{"zone": private},
			Requirements: appconfig.WorkerNetworkRequirements{
				Ingress: []string{"public"}, PublicEgress: "direct", PrivateEgress: "deny",
			},
		}},
	}
	adminConfig(`{
		"workers": {
			"runtime_api_override": true,
			"builder_mode": "kubernetes",
			"preview_limit": 32,
			"validator_pool": "private-b",
			"runtime_pools": [
				{"id":"public-a","access":["public"],"default":true,"private_egress":"deny","public_egress":"direct","validator_configured":false,"network_profiles":["standard"]},
				{"id":"private-b","access":["private"],"default":false,"private_egress":"direct","public_egress":"deny","validator_configured":true,"network_profiles":[]}
			]
		},
		"deployment": {"runtime_replicas":3,"validator_replicas":2}
	}`)
}

func TestSystemRuntimePoolManifestsAuthorizationAndCLI(t *testing.T) {
	s, _ := testServer(t)
	const private = "fixture-never-in-manifests"
	pool := appconfig.WorkerRuntimePool{
		ID: "pool-a", RuntimeService: "runtime-a", RuntimeToken: private, ManagementToken: private,
		Deployment: &appconfig.RuntimePoolDeployment{
			Image: "registry.example.com/krun/runtime:v1", Replicas: 2,
			SecretName: "runtime-a", PeerService: "runtime-a",
		},
	}
	s.Config.InternalListen = "127.0.0.1:18081"
	s.Config.Workers = &appconfig.WorkersConfig{
		Namespace: "workers", RuntimePools: []appconfig.WorkerRuntimePool{pool, {ID: "pool-external"}},
		ArtifactStorage: appconfig.WorkerArtifactStorageConfig{
			Credentials: appconfig.ArtifactStorageCredentials{SecretAccessKey: private},
		},
	}
	const base = "/api/v1/system/config/runtime-pools/"
	path := base + "pool-a/manifests"
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{path, "", 401},
		{path, "krun_cli_viewer", 403},
		{base + "missing/manifests", "krun_cli_admin", 404},
		{base + "pool-external/manifests", "krun_cli_admin", 409},
	} {
		w := request(s, http.MethodGet, tc.path, tc.token, "")
		if w.Code != tc.status {
			t.Fatalf("status for %s = %d, want %d", tc.path, w.Code, tc.status)
		}
	}
	w := request(s, http.MethodGet, path, "krun_cli_admin", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), private) {
		t.Fatalf("manifest API did not return a credential-free preview, status %d", w.Code)
	}
	var manifests domain.RuntimePoolManifests
	if err := json.Unmarshal(w.Body.Bytes(), &manifests); err != nil {
		t.Fatal(err)
	}
	if manifests.Deployment == "" || manifests.ConfigMap == "" || manifests.SupportingResources == "" {
		t.Fatal("manifest API response is incomplete")
	}

	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	t.Setenv("KRUN_TOKEN", "krun_cli_admin")
	var output bytes.Buffer
	args := []string{"--endpoint", server.URL, "--profile", filepath.Join(t.TempDir(), "cli.yaml"), "system", "pool-manifests", pool.ID}
	if err := cli.Run(t.Context(), args, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != manifests.YAML() {
		t.Fatal("CLI preview did not match the API preview")
	}
}
