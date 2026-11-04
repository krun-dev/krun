// Package devruntime renders local-first infrastructure from the main config.
package devruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/adapters/objectstore"
	"github.com/krun-dev/krun/internal/config"
)

type renderOptions struct {
	workers     *config.WorkersConfig
	pool        config.WorkerRuntimePool
	controlHost string
	controlPort string
	storageHost string
	storagePort string
	signed      bool
}

func Render(c config.Config, controlHost, outputDirectory string) error {
	if c.Workers == nil {
		return errors.New("Workers configuration is required")
	}

	controlIP := net.ParseIP(controlHost)
	if controlIP == nil || controlIP.To4() == nil {
		return errors.New("control-host must be a cluster-reachable IPv4 address")
	}
	if outputDirectory == "" {
		return errors.New("output directory is required")
	}
	if err := os.MkdirAll(outputDirectory, 0700); err != nil {
		return err
	}

	options, err := newRenderOptions(c, controlHost)
	if err != nil {
		return err
	}

	replacements := options.templateReplacements()
	for _, name := range []string{"runtime", "validator"} {
		if err := renderTemplate(outputDirectory, name+".capnp", replacements); err != nil {
			return err
		}
	}

	if err := writeResource(outputDirectory, "secrets.json", options.secretResources()); err != nil {
		return err
	}
	if err := writeResource(outputDirectory, "runtime-patch.json", options.runtimePodPatch()); err != nil {
		return err
	}
	if err := options.renderNetworkPolicies(outputDirectory, replacements); err != nil {
		return err
	}

	if !options.signed {
		return renderTemplate(outputDirectory, "static-assets.yaml", replacements)
	}
	return nil
}

func newRenderOptions(c config.Config, controlHost string) (renderOptions, error) {
	options := renderOptions{
		workers:     c.Workers,
		controlHost: controlHost,
		signed:      c.Workers.S3UsesSignedAccess(),
	}

	for _, candidate := range c.Workers.RuntimePools {
		if candidate.Default {
			options.pool = candidate
			break
		}
	}
	if options.pool.ID != "default" {
		return options, errors.New("local-first runtime renderer requires the default pool id default")
	}

	_, controlPort, err := net.SplitHostPort(c.InternalListen)
	if err != nil {
		return options, errors.New("internal_listen must include a port")
	}
	options.controlPort = controlPort

	storage, _ := url.Parse(c.Workers.S3RuntimeEndpoint)
	options.storageHost = storage.Hostname()
	options.storagePort = storage.Port()
	if options.storagePort == "" {
		options.storagePort = "80"
		if storage.Scheme == "https" {
			options.storagePort = "443"
		}
	}

	storageIP := net.ParseIP(options.storageHost)
	if !options.signed && (storageIP == nil || storageIP.To4() == nil || storage.Scheme != "http") {
		return options, errors.New("local-first direct storage requires an HTTP IPv4 endpoint; use s3_access_mode: signed for HTTPS or DNS endpoints")
	}
	if storage.Scheme == "http" && storageIP == nil {
		return options, errors.New("local-first HTTP storage requires a cluster-reachable IP endpoint")
	}
	if storageIP != nil && storageIP.To4() == nil {
		return options, errors.New("local-first storage network policies require an IPv4 endpoint")
	}

	return options, nil
}

func (o renderOptions) templateReplacements() *strings.Replacer {
	allowedNetworks := quote(o.controlHost + "/32")
	if net.ParseIP(o.storageHost) == nil {
		// DNS-backed public S3 endpoints can change addresses.
		// Only the trusted entry worker receives this network capability.
		allowedNetworks += ",\"public\""
	} else {
		allowedNetworks += "," + quote(o.storageHost+"/32")
	}

	return strings.NewReplacer(
		"__OBJECT_STORAGE_BINDINGS__", o.objectStorageBindings(),
		"__OBJECT_STORAGE_SERVICES__", o.objectStorageServices(),
		"__CONTROL_HOST__", o.controlHost,
		"__CONTROL_PORT__", o.controlPort,
		"__MINIO_HOST__", o.storageHost,
		"__MINIO_PORT__", o.storagePort,
		"__STORAGE_ALLOW__", allowedNetworks,
		"__CONFIG_BASE__", quote(o.workers.ConfigBaseURL+"/pools/default"),
		"__STORAGE_BINDINGS__", o.storageBindings(),
	)
}

func (o renderOptions) storageBindings() string {
	if !o.signed {
		return ""
	}

	domains := make(map[string]string)
	for _, domain := range o.workers.Domains {
		if !config.RuntimePoolHasAccess(o.pool, domain.Access) {
			continue
		}

		prefix := "int"
		if domain.Access == "public" {
			prefix = "pub"
		}
		domains[domain.StaticDomain] = prefix
	}

	encodedDomains, _ := json.Marshal(domains)
	return fmt.Sprintf(`
      (name = "STORAGE_ACCESS_KEY", text = embed "/storage/access"),
      (name = "STORAGE_SECRET_KEY", text = embed "/storage/secret"),
      (name = "STORAGE_REGION", text = %s),
      (name = "STATIC_DOMAINS", text = %s),`,
		quote(o.workers.S3Region),
		quote(string(encodedDomains)),
	)
}

func renderTemplate(outputDirectory, name string, replacements *strings.Replacer) error {
	templatePath := filepath.Join("deploy", "workers-dev", name+".tmpl")
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return err
	}
	return writeResource(outputDirectory, name, replacements.Replace(string(raw)))
}

func writeResource(outputDirectory, name string, value any) error {
	var raw []byte
	var err error
	if text, ok := value.(string); ok {
		raw = []byte(text)
	} else {
		raw, err = json.MarshalIndent(value, "", "  ")
	}
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(outputDirectory, name), raw, 0600)
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func CheckStorage(c *config.WorkersConfig) error {
	if c == nil {
		return nil
	}

	client, err := objectstore.NewWorkerS3(c)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// BucketExists is standard S3. A missing managed bucket can be created by
	// the controller; external buckets must already exist.
	exists, err := client.BucketExists(ctx, c.S3Bucket)
	if err != nil {
		return errors.New("configured S3 storage is unavailable or credentials lack bucket access")
	}
	if !exists && c.S3BucketMode == "external" {
		return errors.New("configured external S3 bucket does not exist")
	}
	return nil
}
