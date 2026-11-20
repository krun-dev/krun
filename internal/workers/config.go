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
	"github.com/krun-dev/krun/internal/storage"
)

type EdgeConfigSnapshot struct {
	domain.EdgeConfigCursor
	ManagedRoutes []domain.EdgeConfigRoute        `json:"managed_routes,omitempty"`
	Pool          string                          `json:"pool"`
	Routes        []domain.EdgeConfigRoute        `json:"routes"`
	Versions      map[string]domain.WorkerVersion `json:"versions"`
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
	managedTargets := map[string]domain.EdgeConfigRoute{}
	for id, ws := range st.Workers.Workers {
		resource, ok := st.Resources[id]
		if !ok || resource.Kind != "worker" || resource.Organization != ws.Organization {
			continue
		}
		if _, ok := st.Organizations[ws.Organization]; !ok {
			continue
		}
		ws = config.NormalizeWorkerNetwork(ws)
		ws.AddressName = resource.Locator
		placement, placed := placements[id]
		if !placed || placement.NetworkProfile != ws.NetworkProfile || !config.PoolAllowedForProfile(ws.NetworkProfile, placement.Pool) {
			continue
		}
		pool := placement.Pool
		hosts := WorkerHostPatterns(ws, config)
		for name, env := range ws.Environments {
			// The map key is authoritative: Environment.Name is a denormalised
			// copy; the key defines the environment identity.
			if name == "" {
				continue
			}
			systemRoute := domain.EdgeConfigRoute{
				WorkerGradualRelease: env.WorkerGradualRelease,
				Worker:               id, Path: "/", Enabled: env.Enabled, Version: env.Version, Pool: pool,
				Environment: name, ScriptName: resource.Locator, Retained: env.RetainedAssets,
			}
			if name == appconfig.WorkerBaseline {
				managedTargets[id] = systemRoute
			}
			if ((name == appconfig.WorkerBaseline && !ws.WorkersDevDisabled) || (name != appconfig.WorkerBaseline && !ws.PreviewURLsDisabled)) &&
				(st.Workers.ProjectionWorkers == nil || slices.Contains(st.Workers.ProjectionWorkers, id)) {
				for _, host := range hosts {
					if expanded := EnvHostName(host, name); expanded != "" {
						route := systemRoute
						route.Host = expanded
						route.Public = publicWorkerHost(config, expanded)
						out.Routes = append(out.Routes, route)
					}
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

	previewDomains := map[string][]customPreviewDomain{}
	for _, hostname := range st.Workers.ManagedHostnames {
		suffix, ok := config.DomainForHost(hostname.Host)
		kind := appconfig.WorkerDomainType(hostname.Type)
		if !ok || suffix.ID != hostname.SuffixID || !suffix.Allows(kind) || (kind != appconfig.WorkerDomainCustom && kind != appconfig.WorkerDomainApplication) || hostname.Retired || config.ManagedHostReserved(hostname.Host) {
			continue
		}
		pool, err := config.DomainRuntimePool(suffix.ID)
		if err != nil {
			continue
		}
		for _, rule := range hostname.Routes {
			if rule.Origin != "" || (rule.CustomDomain != (kind == appconfig.WorkerDomainCustom)) {
				continue
			}
			target := managedTargets[rule.Worker]
			worker := st.Workers.Workers[rule.Worker]
			if target.Pool != pool.ID || worker.Organization != hostname.Organization || !config.ManagedDomainAllowsWorker(suffix.ID, worker.NetworkProfile) {
				// Retain the path when its target disappears; a broader route must
				// not start handling this path just because a Worker was deleted.
				target = domain.EdgeConfigRoute{Worker: rule.Worker, Pool: pool.ID}
			}
			if rule.PreviewsEnabled && target.Environment == appconfig.WorkerBaseline {
				previewDomains[rule.Worker] = append(previewDomains[rule.Worker], customPreviewDomain{hostname.Host, suffix.Access == "public"})
			}
			if rule.CustomDomain && rule.PreviewsEnabled && !rule.Enabled {
				continue
			}
			target.Host = hostname.Host
			target.Public = suffix.Access == "public"
			target.Match = rule.Match
			target.Path = rule.Path
			target.Enabled = target.Enabled && rule.Enabled
			out.Routes = append(out.Routes, target)
		}
	}

	for id, domains := range previewDomains {
		worker := st.Workers.Workers[id]
		base := managedTargets[id]
		for name, env := range worker.Environments {
			for _, binding := range domains {
				host := customPreviewHost(binding.host, name, "")
				if host == "" {
					continue
				}
				out.Routes = append(out.Routes, domain.EdgeConfigRoute{
					WorkerGradualRelease: env.WorkerGradualRelease,
					Worker:               id, Host: host, Path: "/", Enabled: env.Enabled, Version: env.Version,
					Pool: base.Pool, Environment: name, ScriptName: base.ScriptName, Public: binding.public,
					Retained: env.RetainedAssets,
				})
			}
		}
	}

	for _, deployment := range st.Workers.Deployments {
		if st.Workers.ProjectionWorkers != nil && !slices.Contains(st.Workers.ProjectionWorkers, deployment.Worker) {
			continue
		}
		if deployment.Committed == nil || !deployment.Expires.After(time.Now()) || deployment.Candidate != "" {
			continue
		}
		worker, ok := st.Workers.Workers[deployment.Worker]
		if !ok {
			continue
		}
		if locator := st.Resources[deployment.Worker].Locator; locator != "" {
			worker.AddressName = locator
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
		for _, binding := range previewDomains[worker.Worker] {
			if host := customPreviewHost(binding.host, deployment.Environment, deployment.ID); host != "" {
				out.Routes = append(out.Routes, domain.EdgeConfigRoute{Worker: worker.Worker, Host: host, Path: "/", Expires: &deployment.Expires, Enabled: env.Enabled, Version: version.ID, Pool: placement.Pool, Public: binding.public})
			}
		}
		out.Versions[version.ID] = runtimeVersionDescriptor(version)
	}
	now := time.Now()
	for _, version := range st.Workers.Versions {
		if st.Workers.ProjectionWorkers != nil && !slices.Contains(st.Workers.ProjectionWorkers, version.Worker) {
			continue
		}
		worker, ok := st.Workers.Workers[version.Worker]
		if !ok || version.Organization != worker.Organization || !worker.Environments[version.Environment].Enabled {
			continue
		}
		if locator := st.Resources[version.Worker].Locator; locator != "" {
			worker.AddressName = locator
		}
		target, active := managedTargets[worker.Worker]
		if !active {
			continue
		}
		for _, host := range VersionURLHosts(config, worker, version, now) {
			expires := version.Created.Add(storage.WorkerArtifactRetain)
			out.Routes = append(out.Routes, domain.EdgeConfigRoute{Worker: worker.Worker, Host: host, Path: "/", Expires: &expires, Enabled: true, Version: version.ID, Pool: target.Pool, Public: publicWorkerHost(config, host)})
			out.Versions[version.ID] = runtimeVersionDescriptor(version)
		}
	}

	sort.Slice(out.Routes, func(i, j int) bool { return edgeRouteLess(out.Routes[i], out.Routes[j]) })
	return out
}

func runtimeVersionDescriptor(version domain.WorkerVersion) domain.WorkerVersion {
	version.VersionURL = false
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
func WorkerHostPatterns(ws domain.Worker, config *appconfig.WorkersConfig) []string {
	selected := map[string]bool{}
	selection := ws.Domains
	if len(selection) == 0 {
		selection = config.NormalizeWorkerNetwork(ws).Domains
	}
	for _, id := range selection {
		selected[id] = true
	}
	slug := ws.AddressName
	out := []string{}
	for _, d := range config.Domains {
		if d.Allows(appconfig.WorkerDomainDefault) && selected[d.ID] {
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
		a.Worker == b.Worker && a.Host == b.Host && a.Path == b.Path && a.Match == b.Match &&
		a.Enabled == b.Enabled && a.Version == b.Version && a.Pool == b.Pool &&
		a.ScriptName == b.ScriptName && a.Environment == b.Environment && a.Public == b.Public &&
		reflect.DeepEqual(a.Expires, b.Expires)
}

func edgeRouteLess(a, b domain.EdgeConfigRoute) bool {
	if a.Host != b.Host {
		return a.Host < b.Host
	}
	return a.Key() < b.Key()
}

type configRouteKey struct {
	host string
	rule string
}

func routeIdentity(route domain.EdgeConfigRoute) configRouteKey {
	return configRouteKey{host: route.Host, rule: route.Key()}
}

// diffWorkerConfig emits the ops needed to move a runtime from prev to next.
// Version descriptors referenced by changed routes are always included, so a
// runtime that pruned an unreferenced version can still apply a rollback.
func DiffWorkerConfig(prev *domain.EdgeConfigProjection, next domain.EdgeConfigProjection) []domain.EdgeConfigOp {
	prevRoutes := map[configRouteKey]domain.EdgeConfigRoute{}
	prevVersions := map[string]domain.WorkerVersion{}
	if prev != nil {
		for _, route := range prev.Routes {
			prevRoutes[routeIdentity(route)] = route
		}
		for id, version := range prev.Versions {
			prevVersions[id] = version
		}
	}
	nextRoutes := map[configRouteKey]domain.EdgeConfigRoute{}
	for _, route := range next.Routes {
		nextRoutes[routeIdentity(route)] = route
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
		old, ok := prevRoutes[routeIdentity(route)]
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
	deleted := []domain.EdgeConfigRoute{}
	for key, previous := range prevRoutes {
		if current, ok := nextRoutes[key]; !ok || current.Pool != previous.Pool {
			deleted = append(deleted, previous)
		}
	}
	sort.Slice(deleted, func(i, j int) bool { return edgeRouteLess(deleted[i], deleted[j]) })
	versionKeys := make([]string, 0, len(neededVersions))
	for key := range neededVersions {
		versionKeys = append(versionKeys, key)
	}
	sort.Strings(versionKeys)
	sort.Slice(changedRoutes, func(i, j int) bool { return edgeRouteLess(changedRoutes[i], changedRoutes[j]) })
	ops := make([]domain.EdgeConfigOp, 0, len(versionKeys)+len(changedRoutes)+len(deleted))
	for _, key := range versionKeys {
		item := neededVersions[key]
		version := item.version
		ops = append(ops, domain.EdgeConfigOp{Type: "version_put", Pool: item.pool, Version: &version})
	}
	for _, route := range deleted {
		ops = append(ops, domain.EdgeConfigOp{Type: "route_delete", Pool: route.Pool, Host: route.Host, RouteKey: route.Key()})
	}
	for i := range changedRoutes {
		route := changedRoutes[i]
		ops = append(ops, domain.EdgeConfigOp{Type: "route_upsert", Pool: route.Pool, Host: route.Host, RouteKey: route.Key(), Route: &route})
	}
	return ops
}

// Keep managed rules separate on the wire. An older Runtime that only reads
// routes cannot accidentally treat the first managed rule as a whole-host route.
func NewEdgeConfigSnapshot(pool string, position domain.EdgeConfigCursor, projection domain.EdgeConfigProjection) EdgeConfigSnapshot {
	snapshot := EdgeConfigSnapshot{
		EdgeConfigCursor: position,
		Pool:             pool,
		Routes:           []domain.EdgeConfigRoute{},
		Versions:         projection.Versions,
	}
	for _, route := range projection.Routes {
		if route.Match == "" {
			snapshot.Routes = append(snapshot.Routes, route)
		} else {
			snapshot.ManagedRoutes = append(snapshot.ManagedRoutes, route)
		}
	}
	return snapshot
}

func EncodeWorkerConfigSnapshot(pool string, position domain.EdgeConfigCursor, p domain.EdgeConfigProjection) ([]byte, error) {
	snapshot := NewEdgeConfigSnapshot(pool, position, p)
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
