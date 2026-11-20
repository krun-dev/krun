package workers

import (
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func TestWorkerVersionAndDeploymentAddresses(t *testing.T) {
	cfg := &config.WorkersConfig{Domains: []config.WorkerDomain{{
		ID: "default", Domain: "workers.test", Types: []config.WorkerDomainType{config.WorkerDomainDefault},
	}}}
	w := domain.Worker{Worker: "worker-id", Organization: "org", AddressName: "orders", Domains: []string{"default"}}
	v := domain.WorkerVersion{ID: "12345678-abcd", VersionURL: true, Created: time.Now()}
	hosts := VersionURLHosts(cfg, w, v, time.Now())
	if len(hosts) != 1 || hosts[0] != "12345678-orders.workers.test" {
		t.Fatalf("Wrangler version URL: %v", hosts)
	}
	deployments := DeploymentHosts(cfg, w, "dep-123")
	if len(deployments) != 1 || deployments[0] != DeploymentEnvironment("dep-123")+"-orders.workers.test" {
		t.Fatalf("deployment URL: %v", deployments)
	}
}
