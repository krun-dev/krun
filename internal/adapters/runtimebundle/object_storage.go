package runtimebundle

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/krun-dev/krun/internal/config"
)

func objectStorageBindings(workers *config.WorkersConfig) string {
	if workers.ObjectStorage == nil {
		return ""
	}
	targets := map[string]map[string]string{}
	var bindings strings.Builder
	for index, store := range workers.ObjectStorage.Stores {
		name := fmt.Sprintf("OBJECT_STORE_%d", index)
		targets[store.ID] = map[string]string{"binding": name}
		fmt.Fprintf(&bindings, "(name = %s, service = %s),\n", quote(name), quote("object-store-"+store.ID))
	}
	raw, _ := json.Marshal(targets)
	fmt.Fprintf(&bindings, "(name = \"OBJECT_STORES\", text = %s),\n", quote(string(raw)))
	return bindings.String()
}

func objectStorageServices(workers *config.WorkersConfig) string {
	if workers.ObjectStorage == nil {
		return ""
	}
	var services strings.Builder
	for _, store := range workers.ObjectStorage.Stores {
		u, _ := url.Parse(store.Endpoint)
		host := strings.ToLower(u.Hostname())
		if store.AddressingStyle == "virtual" {
			host = store.Bucket + "." + host
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		hostHeader := host
		if strings.Contains(host, ":") {
			hostHeader = "[" + host + "]"
		}
		defaultPort := "80"
		if u.Scheme == "https" {
			defaultPort = "443"
		}
		if u.Port() != "" && u.Port() != defaultPort {
			hostHeader = net.JoinHostPort(host, u.Port())
		}
		options := fmt.Sprintf(`(style = host, injectRequestHeaders = [(name = "Host", value = %s)])`, quote(hostHeader))
		protocol := "http = " + options
		if u.Scheme == "https" {
			protocol = fmt.Sprintf("https = (options = %s, certificateHost = %s, tlsOptions = (trustBrowserCas = true))", options, quote(host))
		}
		fmt.Fprintf(&services, "(name = %s, external = (address = %s, %s)),\n", quote("object-store-"+store.ID), quote(net.JoinHostPort(host, port)), protocol)
	}
	return services.String()
}
