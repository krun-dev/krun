package workers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
)

type EdgeConfigSnapshot struct {
	domain.EdgeConfigCursor
	Pool     string                          `json:"pool"`
	Routes   []domain.EdgeConfigRoute        `json:"routes"`
	Versions map[string]domain.WorkerVersion `json:"versions"`
}

const (
	WorkerConfigPoolsDir = "config/pools/"
)

func WorkerPoolConfigPrefix(pool, epoch string) string {
	return WorkerConfigPoolsDir + pool + "/epochs/" + epoch + "/"
}

func WorkerPoolConfigLatestKey(pool, epoch string) string {
	return WorkerPoolConfigPrefix(pool, epoch) + "latest.json"
}

// workerConfigFromState includes the baseline and preview environments of every
// live worker. Disabled environments stay in the projection so the runtime can
// answer 410 instead of 404; unpublished environments carry an empty version.
//
// Hosts are not stored, so they are derived here from the configured domain
// catalog and the worker's selection. This is the same derivation the
// HTTPRoute reconciler and the API use, which keeps the three from drifting.
func EdgeConfigFromState(st *domain.State, config *appconfig.WorkersConfig) domain.EdgeConfigProjection {
	out := domain.EdgeConfigProjection{Routes: []domain.EdgeConfigRoute{}, Versions: map[string]domain.WorkerVersion{}}
	if st == nil || st.Workers == nil {
		return out
	}
	placements := map[string]domain.WorkerPlacement{}
	for _, p := range st.Workers.Placements {
		if p.Role == "primary" && p.State == "active" {
			placements[p.Worker] = p
		}
	}
	for id, ws := range st.Workers.Workers {
		resource, ok := st.Resources[id]
		if !ok || resource.Kind != "worker" || resource.Organization != ws.Organization {
			continue
		}
		if _, ok := st.Organizations[ws.Organization]; !ok {
			continue
		}
		ws = config.NormalizeWorkerNetwork(ws)
		placement, placed := placements[id]
		if !placed || placement.NetworkProfile != ws.NetworkProfile || !config.PoolAllowedForProfile(ws.NetworkProfile, placement.Pool) {
			continue
		}
		pool := placement.Pool
		hosts := WorkerHostPatterns(ws, config.Domains)
		for name, env := range ws.Environments {
			// The map key is authoritative: Environment.Name is a denormalised
			// copy; the key defines the environment identity.
			if name == "" {
				continue
			}
			// One route per selected domain. The runtime matches purely on Host;
			// the Gateway instance already decided which entry a request used.
			for _, host := range hosts {
				if expanded := EnvHostName(host, name); expanded != "" {
					route := domain.EdgeConfigRoute{
						WorkerGradualRelease: env.WorkerGradualRelease,
						Worker:               id, Host: expanded, Path: "/", Enabled: env.Enabled,
						Version: env.Version, Pool: pool, Environment: name, ScriptName: resource.Locator,
						Public: publicWorkerHost(config, expanded), Retained: env.RetainedAssets,
					}
					out.Routes = append(out.Routes, route)
				}
			}
			if env.Version == "" {
				continue
			}
			targets := []string{env.Version, env.Candidate}
			for _, item := range env.RetainedAssets {
				targets = append(targets, item.Version)
			}
			for _, target := range targets {
				if version, ok := st.Workers.Versions[target]; ok && version.Worker == id && version.Organization == ws.Organization {
					out.Versions[version.ID] = runtimeVersionDescriptor(version)
				}
			}
		}
	}

	for _, deployment := range st.Workers.Deployments {
		if deployment.Committed == nil || !deployment.Expires.After(time.Now()) || deployment.Candidate != "" {
			continue
		}
		worker, ok := st.Workers.Workers[deployment.Worker]
		if !ok {
			continue
		}
		env, ok := worker.Environments[deployment.Environment]
		if !ok {
			continue
		}
		placement, ok := placements[worker.Worker]
		if !ok || !config.PoolAllowedForProfile(worker.NetworkProfile, placement.Pool) {
			continue
		}
		version, ok := st.Workers.Versions[deployment.Version]
		if !ok || version.Worker != worker.Worker || version.Organization != worker.Organization {
			continue
		}
		for _, host := range DeploymentHosts(config, worker, deployment.ID) {
			out.Routes = append(out.Routes, domain.EdgeConfigRoute{Worker: worker.Worker, Host: host, Path: "/", Expires: &deployment.Expires, Enabled: env.Enabled, Version: version.ID, Pool: placement.Pool})
		}
		out.Versions[version.ID] = runtimeVersionDescriptor(version)
	}
	sort.Slice(out.Routes, func(i, j int) bool { return out.Routes[i].Host < out.Routes[j].Host })
	return out
}

func runtimeVersionDescriptor(version domain.WorkerVersion) domain.WorkerVersion {
	version.ServiceBindings = maps.Clone(version.ServiceBindings)
	for name, binding := range version.ServiceBindings {
		binding.GrantedBy = nil
		version.ServiceBindings[name] = binding
	}
	return version
}

func EdgeConfigOpsForPool(in []domain.EdgeConfigOp, pool string) []domain.EdgeConfigOp {
	out := make([]domain.EdgeConfigOp, 0, len(in))
	for _, op := range in {
		if op.Pool == pool {
			out = append(out, op)
		}
	}
	return out
}

// workerHostPatterns returns one "<slug>\x00<suffix>" pattern per selected
// domain, in catalog order. envHostName expands the environment segment. A
// worker with no stored selection falls back to the catalog defaults, matching
// what normalizeWorker does for the API and the route reconciler.
func WorkerHostPatterns(ws domain.Worker, domains []appconfig.WorkerDomain) []string {
	selected := map[string]bool{}
	selection := ws.Domains
	if len(selection) == 0 {
		for _, domain := range domains {
			if domain.Default {
				selection = append(selection, domain.ID)
			}
		}
		if len(selection) == 0 && len(domains) > 0 {
			selection = []string{domains[0].ID}
		}
	}
	for _, id := range selection {
		selected[id] = true
	}
	slug := appconfig.WorkerSlug(ws.Organization, ws.Worker)
	out := []string{}
	for _, d := range domains {
		if selected[d.ID] {
			out = append(out, slug+"\x00"+d.Domain)
		}
	}
	return out
}

func EnvHostName(pattern, env string) string {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == 0 {
			return appconfig.HostFor(pattern[:i], env, pattern[i+1:])
		}
	}
	return ""
}

func publicWorkerHost(config *appconfig.WorkersConfig, host string) bool {
	for _, d := range config.Domains {
		if _, suffix, ok := strings.Cut(host, "."); ok && suffix == d.Domain {
			return d.Access == "public"
		}
	}
	return false
}

func SameWorkerConfigRoute(a, b domain.EdgeConfigRoute) bool {
	if !slices.Equal(a.Retained, b.Retained) {
		return false
	}
	return a.WorkerGradualRelease == b.WorkerGradualRelease &&
		a.Worker == b.Worker && a.Host == b.Host && a.Path == b.Path &&
		a.Enabled == b.Enabled && a.Version == b.Version && a.Pool == b.Pool &&
		a.ScriptName == b.ScriptName && a.Environment == b.Environment && a.Public == b.Public &&
		reflect.DeepEqual(a.Expires, b.Expires)
}

// diffWorkerConfig emits the ops needed to move a runtime from prev to next.
// Version descriptors referenced by changed routes are always included, so a
// runtime that pruned an unreferenced version can still apply a rollback.
func DiffWorkerConfig(prev *domain.EdgeConfigProjection, next domain.EdgeConfigProjection) []domain.EdgeConfigOp {
	prevRoutes := map[string]domain.EdgeConfigRoute{}
	prevVersions := map[string]domain.WorkerVersion{}
	if prev != nil {
		for _, route := range prev.Routes {
			prevRoutes[route.Host] = route
		}
		for id, version := range prev.Versions {
			prevVersions[id] = version
		}
	}
	nextRoutes := map[string]domain.EdgeConfigRoute{}
	for _, route := range next.Routes {
		nextRoutes[route.Host] = route
	}
	changedRoutes := []domain.EdgeConfigRoute{}
	type PooledVersion struct {
		pool    string
		version domain.WorkerVersion
	}
	neededVersions := map[string]PooledVersion{}
	addVersion := func(pool, id string) {
		if version, ok := next.Versions[id]; ok {
			neededVersions[pool+"\x00"+id] = PooledVersion{pool: pool, version: version}
		}
	}
	for _, route := range next.Routes {
		old, ok := prevRoutes[route.Host]
		if ok && SameWorkerConfigRoute(old, route) {
			continue
		}
		changedRoutes = append(changedRoutes, route)
		for _, id := range route.VersionIDs() {
			addVersion(route.Pool, id)
		}
	}
	for id, version := range next.Versions {
		if old, ok := prevVersions[id]; !ok || !reflect.DeepEqual(old, version) {
			for _, route := range next.Routes {
				if slices.Contains(route.VersionIDs(), id) {
					addVersion(route.Pool, id)
				}
			}
		}
	}
	deleted := []string{}
	for host := range prevRoutes {
		if _, ok := nextRoutes[host]; !ok {
			deleted = append(deleted, host)
		}
	}
	sort.Strings(deleted)
	versionKeys := make([]string, 0, len(neededVersions))
	for key := range neededVersions {
		versionKeys = append(versionKeys, key)
	}
	sort.Strings(versionKeys)
	sort.Slice(changedRoutes, func(i, j int) bool { return changedRoutes[i].Host < changedRoutes[j].Host })
	ops := make([]domain.EdgeConfigOp, 0, len(versionKeys)+len(changedRoutes)+len(deleted))
	for _, key := range versionKeys {
		item := neededVersions[key]
		version := item.version
		ops = append(ops, domain.EdgeConfigOp{Type: "version_put", Pool: item.pool, Version: &version})
	}
	for i := range changedRoutes {
		route := changedRoutes[i]
		ops = append(ops, domain.EdgeConfigOp{Type: "route_upsert", Pool: route.Pool, Host: route.Host, Route: &route})
	}
	for _, host := range deleted {
		ops = append(ops, domain.EdgeConfigOp{Type: "route_delete", Pool: prevRoutes[host].Pool, Host: host})
	}
	return ops
}

func EncodeWorkerConfigSnapshot(pool string, position domain.EdgeConfigCursor, p domain.EdgeConfigProjection) ([]byte, error) {
	snapshot := EdgeConfigSnapshot{EdgeConfigCursor: position, Pool: pool, Routes: p.Routes, Versions: p.Versions}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type EdgeConfigLatest struct {
	Epoch          string `json:"config_epoch"`
	LazyRoutes     bool   `json:"lazy_routes,omitempty"`
	ConfigRevision int64  `json:"config_revision"`
	Snapshot       string `json:"snapshot"`
	SHA256         string `json:"sha256"`
	OpsFloor       int64  `json:"ops_floor"`
}

// publishConfig serializes local publication; PostgreSQL coordinates replicas
// through the per-worker dirty queue and revision CAS.
func (w *Controller) PublishConfig(ctx context.Context) (int64, error) {
	w.configMu.Lock()
	defer w.configMu.Unlock()
	return w.PublishConfigDelta(ctx)
}

func (w *Controller) WriteConfigLatest(ctx context.Context, key string, next EdgeConfigLatest) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if w.S3 == nil {
		return w.PutBytes(ctx, key, raw, objectstore.PutObjectOptions{ContentType: "application/json", CacheControl: "no-store"})
	}
	// The pointer is the only mutable object. Use the object's ETag as a second
	// CAS so an older publisher cannot overwrite a newer revision between GET
	// and PUT. A precondition race is retried after observing the winner.
	for attempts := 0; attempts < 4; attempts++ {
		stat, statErr := w.S3.StatObject(ctx, w.Config.S3Bucket, key, objectstore.StatObjectOptions{})
		opts := objectstore.PutObjectOptions{ContentType: "application/json", CacheControl: "no-store"}
		if statErr == nil {
			currentRaw, readErr := w.GetObject(ctx, key)
			if readErr != nil {
				return readErr
			}
			var current EdgeConfigLatest
			if json.Unmarshal(currentRaw, &current) == nil {
				if current.ConfigRevision > next.ConfigRevision {
					return nil
				}
				if current.ConfigRevision == next.ConfigRevision && current.Snapshot == next.Snapshot && current.SHA256 == next.SHA256 && current.OpsFloor == next.OpsFloor {
					return nil
				}
			}
			opts.SetMatchETag(stat.ETag)
		} else if code := objectstore.ToErrorResponse(statErr).Code; code == "NoSuchKey" || code == "NoSuchObject" || code == "NotFound" {
			opts.SetMatchETagExcept("*")
		} else {
			return statErr
		}
		if err := w.PutBytes(ctx, key, raw, opts); err == nil {
			return nil
		} else if objectstore.ToErrorResponse(err).Code != "PreconditionFailed" {
			return err
		}
	}
	return errors.New("edge config pointer changed repeatedly")
}
