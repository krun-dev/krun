// Package runtimebundle renders the same workerd entrypoint for development
// and Kubernetes deployment. It never embeds issuer or database credentials.
package runtimebundle

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/krun-dev/krun/internal/config"
)

//go:embed templates/*.capnp
var templates embed.FS

func Render(workers *config.WorkersConfig, pool config.WorkerRuntimePool, controlAddress string, networks []string, validator bool) (string, error) {
	origin, err := workers.RuntimeAPIOrigin()
	if err != nil {
		return "", err
	}
	controlExternal := fmt.Sprintf("(address = %s, http = ())", quote(controlAddress))
	if origin != nil {
		port := origin.Port()
		if port == "" {
			port = "443"
		}
		controlExternal = fmt.Sprintf(
			`(address = %s, https = (options = (injectRequestHeaders = [(name = "Host", value = %s)]), certificateHost = %s, tlsOptions = (trustBrowserCas = true)))`,
			quote(net.JoinHostPort(origin.Hostname(), port)),
			quote(origin.Host),
			quote(origin.Hostname()),
		)
	}
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
	egressBinding := ""
	egressService := ""
	denied := make([]string, 0, len(workers.ProtectedEgressCIDRs))
	for _, cidr := range workers.ProtectedEgressCIDRs {
		denied = append(denied, quote(cidr))
	}
	if !validator && pool.PublicEgress == "direct" {
		egressBinding = `          (name = "PUBLIC_EGRESS", service = "worker-public-egress"),`
		egressService = fmt.Sprintf(`    (
      name = "worker-public-egress",
      network = (
        allow = ["public"],
        deny = [%s],
        tlsOptions = (trustBrowserCas = true)
      )
    ),`, strings.Join(denied, ","))
	}
	if !validator && workers.Hyperdrive != nil && (pool.PublicEgress == "direct" || pool.PrivateEgress == "direct") {
		egressBinding += "\n          (name = \"HYPERDRIVE_EGRESS\", service = \"worker-hyperdrive-egress\"),"
		allow := []string{quote(workers.Hyperdrive.CIDR())}
		if pool.PublicEgress == "direct" {
			allow = append(allow, quote("public"))
		}
		egressService += fmt.Sprintf(`
    (
      name = "worker-hyperdrive-egress",
      network = (
        allow = [%s],
        deny = [%s],
        tlsOptions = (trustBrowserCas = true)
      )
    ),`, strings.Join(allow, ","), strings.Join(denied, ","))
	}
	replacer := strings.NewReplacer(
		"__PUBLIC_EGRESS_BINDING__", egressBinding,
		"__PUBLIC_EGRESS_SERVICE__", egressService,
		"__CONTROL_EXTERNAL__", controlExternal,
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
