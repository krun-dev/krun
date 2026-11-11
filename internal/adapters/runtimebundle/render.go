// Package runtimebundle renders the same workerd entrypoint for development
// and Kubernetes deployment. It never embeds issuer or database credentials.
package runtimebundle

import (
	"crypto/x509"
	"embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
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
	if !validator && (pool.PublicEgress == "direct" || pool.PrivateEgress == "direct") {
		egressBinding += "\n          (name = \"HYPERDRIVE_DIRECT_EGRESS\", service = \"worker-hyperdrive-egress\"),"
		var allow []string
		if pool.PublicEgress == "direct" {
			allow = append(allow, quote("public"))
		}
		if pool.PrivateEgress == "direct" {
			allow = append(allow, privateDatabaseNetworks(workers.ProtectedEgressCIDRs)...)
		} else {
			denied = append(denied, quote("100.64.0.0/10"))
		}
		// Keep the private egress capability away from the host and special-use
		// ranges even when the pool allows access to private networks.
		for _, target := range []string{
			"local", "0.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
			"::/128", "fe80::/10", "ff00::/8", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2002::/16",
		} {
			denied = append(denied, quote(target))
		}
		controlHost, _, _ := net.SplitHostPort(controlAddress)
		controlHosts := []string{controlHost}
		if origin != nil {
			controlHosts = append(controlHosts, origin.Hostname())
		}
		for _, host := range controlHosts {
			if addr, err := netip.ParseAddr(host); err == nil {
				addr = addr.Unmap()
				denied = append(denied, quote(netip.PrefixFrom(addr, addr.BitLen()).String()))
			}
		}
		tlsOptions, err := databaseTLSOptions(workers.DatabaseOriginCAFile)
		if err != nil {
			return "", err
		}
		egressService += fmt.Sprintf(`
    (
      name = "worker-hyperdrive-egress",
      network = (
        allow = [%s],
        deny = [%s],
        tlsOptions = (%s)
      )
    ),`, strings.Join(allow, ","), strings.Join(denied, ","), tlsOptions)
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

// KJ gives a more-specific allow priority over a broader deny. Omit any
// private allow range fully covered by an operator's protected prefix.
func privateDatabaseNetworks(protected []string) []string {
	var allowed []string
	for _, cidr := range []string{"10.0.0.0/8", "100.64.0.0/10", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"} {
		network := netip.MustParsePrefix(cidr)
		blocked := false
		for _, entry := range protected {
			prefix := netip.MustParsePrefix(entry)
			if prefix.Bits() <= network.Bits() && prefix.Contains(network.Addr()) {
				blocked = true
				break
			}
		}
		if !blocked {
			allowed = append(allowed, quote(cidr))
		}
	}
	return allowed
}

func databaseTLSOptions(filename string) (string, error) {
	const defaults = "trustBrowserCas = true"
	if filename == "" {
		return defaults, nil
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("read workers.database_origin_ca_file: %w", err)
	}
	var certificates []string
	for {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return "", errors.New("workers.database_origin_ca_file must contain only CA certificates")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA {
			return "", errors.New("workers.database_origin_ca_file contains an invalid CA certificate")
		}
		publicPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
		certificates = append(certificates, quote(string(publicPEM)))
		raw = rest
	}
	if len(certificates) == 0 {
		return "", errors.New("workers.database_origin_ca_file contains no CA certificates")
	}
	return defaults + ", trustedCertificates = [" + strings.Join(certificates, ",") + "]", nil
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
