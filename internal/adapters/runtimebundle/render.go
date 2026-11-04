// Package runtimebundle renders the same workerd entrypoint for development
// and Kubernetes deployment. It never embeds issuer or database credentials.
package runtimebundle

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/krun-dev/krun/internal/config"
)

//go:embed templates/*.capnp
var templates embed.FS

func Render(workers *config.WorkersConfig, pool config.WorkerRuntimePool, controlAddress string, networks []string, validator bool) (string, error) {
	name := "runtime"
	loader := "krun-" + pool.ID
	if validator {
		name = "validator"
		loader += "-validator"
	}
	raw, err := templates.ReadFile("templates/" + name + ".capnp")
	if err != nil {
		return "", err
	}
	allowed := make([]string, 0, len(networks))
	for _, network := range networks {
		allowed = append(allowed, quote(network))
	}
	bindings := ""
	services := ""
	if !validator {
		bindings = objectStorageBindings(workers)
		services = objectStorageServices(workers)
	}
	replacer := strings.NewReplacer(
		"__CONTROL_ADDRESS__", quote(controlAddress),
		"__STORAGE_ALLOW__", strings.Join(allowed, ","),
		"__POOL__", quote(pool.ID),
		"__LOADER_ID__", quote(loader),
		"__CONFIG_BASE__", quote(workers.ConfigBaseURL+"/pools/"+pool.ID),
		"__STORAGE_BINDINGS__", storageBindings(workers, pool),
		"__OBJECT_STORAGE_BINDINGS__", bindings,
		"__OBJECT_STORAGE_SERVICES__", services,
	)
	return replacer.Replace(string(raw)), nil
}

func storageBindings(workers *config.WorkersConfig, pool config.WorkerRuntimePool) string {
	if !workers.S3UsesSignedAccess() {
		return ""
	}
	domains := map[string]string{}
	for _, domain := range workers.Domains {
		owner, err := workers.DomainRuntimePool(domain.ID)
		if err != nil || owner.ID != pool.ID {
			continue
		}
		prefix := "int"
		if domain.Access == "public" {
			prefix = "pub"
		}
		domains[domain.StaticDomain] = prefix
	}
	raw, _ := json.Marshal(domains)
	return fmt.Sprintf(`
          (name = "STORAGE_ACCESS_KEY", text = embed "/storage/access"),
          (name = "STORAGE_SECRET_KEY", text = embed "/storage/secret"),
          (name = "STORAGE_REGION", text = %s),
          (name = "STATIC_DOMAINS", text = %s),`, quote(workers.S3Region), quote(string(raw)))
}

func quote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
