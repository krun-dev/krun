package control

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

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
		KubernetesAPI: private, KubernetesTokenFile: private, S3AccessKey: private, S3SecretKey: private,
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
