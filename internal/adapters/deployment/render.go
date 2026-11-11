// Package deployment renders an internal Kubernetes installation from LoadConfig.
// It never applies resources, creates cloud infrastructure or resolves DNS.
package deployment

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/krun-dev/krun/internal/securetoken"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/krun-dev/krun/internal/adapters/runtimebundle"
	"github.com/krun-dev/krun/internal/config"
)

type renderer struct {
	cfg     config.Config
	ns      string
	pool    config.WorkerRuntimePool
	gateway config.WorkerGateway
	items   []any
}

func Render(c config.Config, directory string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := validate(c); err != nil {
		return err
	}
	if directory == "" {
		return errors.New("private output directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	current, _ := os.Getwd()
	for parent := current; ; parent = filepath.Dir(parent) {
		if _, statErr := os.Stat(filepath.Join(parent, ".git")); statErr == nil {
			relative, relErr := filepath.Rel(parent, absolute)
			if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != ".data" && !strings.HasPrefix(relative, ".data"+string(filepath.Separator)) {
				return errors.New("secret deployment output inside a checkout must be under .data")
			}
			break
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return err
	}
	r := renderer{
		cfg:     c,
		ns:      c.Workers.Namespace,
		pool:    c.Workers.RuntimePools[0],
		gateway: consoleGateway(c),
	}
	if err := r.resources(); err != nil {
		return err
	}
	manifest := map[string]any{"apiVersion": "v1", "kind": "List", "items": r.items}
	if err := write(directory, "resources.json", manifest); err != nil {
		return err
	}
	summary := []any{}
	for _, item := range r.items {
		value := item.(map[string]any)
		summary = append(summary, map[string]any{"kind": value["kind"], "metadata": value["metadata"]})
	}
	cors := []any{}
	if c.Workers.ObjectStorage != nil {
		for _, store := range c.Workers.ObjectStorage.Stores {
			rule := map[string]any{
				"AllowedOrigins": []string{c.PublicURL},
				"AllowedMethods": []string{"GET", "HEAD", "PUT"},
				"AllowedHeaders": []string{
					"content-type",
					"content-language",
					"content-disposition",
					"content-encoding",
					"cache-control",
					"expires",
					"range",
					"x-amz-meta-krun-v1",
				},
				"ExposeHeaders": []string{"ETag", "Content-Length", "Content-Range", "Last-Modified"},
				"MaxAgeSeconds": 600,
			}
			cors = append(cors, map[string]any{
				"store":  store.ID,
				"bucket": store.Bucket,
				"configuration": map[string]any{
					"CORSRules": []any{
						rule,
					},
				},
			})
		}
	}
	if err := write(directory, "browser-cors.json", cors); err != nil {
		return err
	}
	gateways := []any{}
	for _, g := range c.Workers.Gateways {
		gateways = append(gateways, map[string]any{"id": g.ID, "name": g.Name, "namespace": g.Namespace, "listener": g.Section, "port": gatewayPort(g)})
	}
	domains := []any{}
	for _, profile := range c.Workers.NetworkProfiles {
		for _, domain := range c.Workers.DomainsForProfile(profile) {
			for _, pool := range c.Workers.PoolsForProfile(profile.ID) {
				domains = append(domains, map[string]any{"profile": profile.ID, "pool": pool.ID, "gateway": domain.Gateway, "worker_dns_suffix": domain.Domain, "static_domain": domain.StaticDomain})
			}
		}
	}
	return write(directory, "summary.json", map[string]any{
		"resources":        summary,
		"console_url":      c.PublicURL,
		"console_gateway":  r.gateway.ID,
		"gateways":         gateways,
		"domains":          domains,
		"contains_secrets": "resources.json contains resolved credentials; do not commit or print it",
	})
}

func validate(c config.Config) error {
	d := c.Deployment
	if d == nil || c.Workers == nil {
		return errors.New("main configuration requires deployment and workers sections")
	}
	if c.Development || c.StaticDir != "" || !strings.HasPrefix(c.PublicURL, "https://") {
		return errors.New("deployment requires development=false, HTTPS public_url and empty static_dir")
	}
	w := c.Workers
	if w.BuilderMode != "kubernetes" || w.BuilderPullPolicy == "Never" || w.S3BucketMode != "external" {
		return errors.New("deployment requires Kubernetes builder, pullable images and external signed object storage")
	}
	if len(w.RuntimePools) == 0 || len(w.Gateways) == 0 || len(w.Domains) == 0 {
		return errors.New("deployment requires pools, Gateways and domains")
	}
	if consoleGateway(c).ID == "" {
		return errors.New("deployment.console_gateway must select a configured Gateway when multiple Gateways exist")
	}
	for _, g := range w.Gateways {
		if w.Namespace == g.Namespace {
			return errors.New("use a dedicated krun namespace, separate from every Gateway namespace")
		}
	}
	if w.Namespace == "default" || strings.HasPrefix(w.Namespace, "kube-") {
		return errors.New("use a dedicated krun namespace, separate from the Gateway namespace")
	}
	for _, address := range []string{c.Listen, c.InternalListen} {
		host, _, err := net.SplitHostPort(address)
		if err != nil || (host != "0.0.0.0" && host != "::") {
			return errors.New("Kubernetes listeners must bind all interfaces")
		}
	}
	if port(c.Listen) == port(c.InternalListen) {
		return errors.New("public and internal listeners require different ports")
	}
	if w.KubernetesKubeconfigFile != "" ||
		w.KubernetesTokenFile != "/var/run/secrets/kubernetes.io/serviceaccount/token" ||
		w.KubernetesCAFile != "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt" {
		return errors.New("deployment requires in-cluster Kubernetes credentials")
	}
	if w.CloneDirectory != "/workdir" {
		return errors.New("deployment clone_directory must be /workdir")
	}
	for _, g := range w.Gateways {
		if value := g.Base; value != "" {
			u, err := url.Parse(value)
			service := g.Name + "." + g.Namespace + ".svc"
			if err != nil || u.Scheme != "http" || (u.Hostname() != service && u.Hostname() != service+".cluster.local") || u.User != nil || u.Path != "" || u.RawQuery != "" {
				return errors.New("Gateway base must be its cluster-native HTTP Service origin")
			}
		}
	}
	services := map[string]bool{"krun-control": true, "krun-control-internal": true}
	for _, p := range w.RuntimePools {
		if p.ValidatorManagementBase != "" {
			return errors.New("deployment requires cluster-native validator addresses")
		}
		for _, name := range []string{p.RuntimeService, p.ValidatorService} {
			if name == "" {
				continue
			}
			if services[name] {
				return errors.New("deployment runtime and validator service names must be unique and not reserved")
			}
			services[name] = true
		}
		for _, value := range []string{p.RuntimeToken, p.ManagementToken} {
			if value == "" || strings.Contains(value, "replace-me") || strings.Contains(value, "replace-with-") {
				return errors.New("deployment pool credentials still contain example placeholders")
			}
		}
		if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$`).MatchString(p.ID) {
			return errors.New("deployment pool ID must be a lowercase DNS label up to 40 characters")
		}
	}
	for _, provider := range c.Providers {
		if provider.IsEnabled() && provider.Type == "dev" {
			return errors.New("development identity providers cannot be deployed")
		}
	}
	enabled := false
	for _, provider := range c.Providers {
		enabled = enabled || provider.IsEnabled()
	}
	if !enabled {
		return errors.New("enable a company login provider before deployment")
	}
	for _, image := range []string{w.BuilderImage, d.ControlImage, d.RuntimeImage} {
		last := image[strings.LastIndex(image, "/")+1:]
		if !strings.Contains(last, ":") || strings.HasSuffix(image, ":latest") || strings.Contains(image, "replace-me") || strings.ContainsAny(image, " \n\r\t$") {
			return errors.New("all deployed images require explicit release tags or digests")
		}
	}
	for _, value := range []string{
		c.DB,
		w.S3AccessKey,
		w.S3SecretKey,
	} {
		if value == "" || strings.Contains(value, "replace-me") || strings.Contains(value, "replace-with-") {
			return errors.New("deployment credentials still contain example placeholders")
		}
	}
	for _, value := range w.SecretsKeys {
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(raw) != 32 || bytes.Equal(raw, make([]byte, 32)) {
			return errors.New("set non-example Worker encryption keys before deployment")
		}
	}
	if w.ObjectStorage != nil {
		raw, err := base64.RawURLEncoding.DecodeString(w.ObjectStorage.CursorKey)
		if err != nil || len(raw) != 32 || bytes.Equal(raw, make([]byte, 32)) {
			return errors.New("set a non-example object storage cursor key before deployment")
		}
		for _, store := range w.ObjectStorage.Stores {
			if strings.Contains(store.Credentials.AccessKeyID, "replace-me") || strings.Contains(store.Credentials.SecretAccessKey, "replace-me") {
				return errors.New("object storage issuer credentials still contain placeholders")
			}
		}
	}
	for _, provider := range c.Providers {
		if provider.IsEnabled() && (strings.Contains(provider.ClientID, "replace-me") || strings.Contains(provider.ClientSecret, "replace-me")) {
			return errors.New("company login credentials still contain placeholders")
		}
	}
	for _, provider := range w.RepositoryProviders {
		if !provider.Enabled {
			continue
		}
		for _, value := range []string{provider.ClientID, provider.ClientSecret, provider.Token} {
			if strings.Contains(value, "replace-me") || strings.Contains(value, "replace-with-") {
				return errors.New("repository credentials still contain placeholders")
			}
		}
	}
	// The mounted main YAML is fully resolved. A dollar must survive the next
	// LoadConfig expansion literally (including passwords with ${...} text).
	return nil
}

func secretData(values map[string]string) map[string]string {
	encoded := make(map[string]string, len(values))
	for key, value := range values {
		encoded[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return encoded
}

func write(directory, name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(directory, name)
	file, err := os.CreateTemp(directory, ".krun-render-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(raw, '\n'))
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporary, target)
}

func (r *renderer) resource(api, kind, name, namespace string, fields map[string]any) map[string]any {
	meta := map[string]any{
		"name": name,
		"labels": map[string]string{
			"app.kubernetes.io/part-of": "krun",
		},
	}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	result := map[string]any{"apiVersion": api, "kind": kind, "metadata": meta}
	for key, value := range fields {
		result[key] = value
	}
	return result
}
func (r *renderer) add(api, kind, name string, fields map[string]any) {
	r.items = append(r.items, r.resource(api, kind, name, r.ns, fields))
}
func hash(value any) string {
	raw, _ := json.Marshal(value)
	return securetoken.DigestBytes(raw)
}

// LoadConfig expands dollar expressions once more inside the container. Escape
// every resolved dollar, so rendering is a value-preserving transformation.
func escapeDollars(value any) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, "$", "$$")
	case []any:
		for i := range v {
			v[i] = escapeDollars(v[i])
		}
		return v
	case map[string]any:
		for key, item := range v {
			v[key] = escapeDollars(item)
		}
		return v
	default:
		return value
	}
}

func (r *renderer) resources() error {
	r.items = append(r.items, r.resource("v1", "Namespace", r.ns, "", nil))
	raw, _ := json.Marshal(r.cfg)
	var app any
	if err := json.Unmarshal(raw, &app); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(escapeDollars(app), "", "  ")
	if err != nil {
		return err
	}
	r.add("v1", "Secret", "krun-control-config", map[string]any{
		"type": "Opaque",
		"data": secretData(map[string]string{
			"control.yaml": string(encoded),
		}),
	})
	r.rbac()
	r.controlDeployment(string(encoded))
	networks := runtimeNetworks(r.cfg.Deployment.Network.Runtime)
	controlAddress := net.JoinHostPort("krun-control-internal."+r.ns+".svc", port(r.cfg.InternalListen))
	for _, pool := range r.cfg.Workers.RuntimePools {
		r.pool = pool
		for _, validator := range []bool{false, true} {
			if validator && r.cfg.Workers.ValidatorPool != pool.ID {
				continue
			}
			text, err := runtimebundle.Render(r.cfg.Workers, r.pool, controlAddress, networks, validator)
			if err != nil {
				return err
			}
			r.runtimeDeployment(text, validator)
		}
	}
	r.consoleRoute()
	r.runtimeAPIRoute()
	r.networkPolicies()
	return nil
}
func port(address string) string     { _, p, _ := net.SplitHostPort(address); return p }
func integerPort(address string) int { var p int; fmt.Sscan(port(address), &p); return p }
func runtimeNetworks(rules []config.DeploymentEgress) []string {
	set := map[string]bool{}
	for _, rule := range rules {
		if rule.PublicHTTPS {
			set["public"] = true
		}
		for _, cidr := range rule.CIDRs {
			set[cidr] = true
		}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
func consoleHost(c config.Config) string { u, _ := url.Parse(c.PublicURL); return u.Hostname() }
