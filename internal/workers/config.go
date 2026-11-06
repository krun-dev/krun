package workers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	objectstore "github.com/krun-dev/krun/internal/adapters/objectstore"
	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
	storage "github.com/krun-dev/krun/internal/storage"
)

type WorkerConfigSnapshot struct {
	ConfigRevision int64                           `json:"config_revision"`
	Pool           string                          `json:"pool"`
	Routes         []domain.WorkerConfigRoute      `json:"routes"`
	Versions       map[string]domain.WorkerVersion `json:"versions"`
}

const (
	WorkerConfigPoolsDir = "config/pools/"
)

func WorkerPoolConfigPrefix(pool string) string { return WorkerConfigPoolsDir + pool + "/" }

func WorkerPoolConfigLatestKey(pool string) string {
	return WorkerPoolConfigPrefix(pool) + "latest.json"
}

// workerConfigFromState includes the baseline and preview environments of every
// live worker. Disabled environments stay in the projection so the runtime can
// answer 410 instead of 404; unpublished environments carry an empty version.
//
// Hosts are not stored, so they are derived here from the configured domain
// catalog and the worker's selection. This is the same derivation the
// HTTPRoute reconciler and the API use, which keeps the three from drifting.
func WorkerConfigFromState(st *domain.State, config *appconfig.WorkersConfig) domain.WorkerConfigProjection {
	out := domain.WorkerConfigProjection{Routes: []domain.WorkerConfigRoute{}, Versions: map[string]domain.WorkerVersion{}}
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
					out.Routes = append(out.Routes, domain.WorkerConfigRoute{Worker: id, Host: expanded, Path: "/", Enabled: env.Enabled, Version: env.Version, Pool: pool})
				}
			}
			if env.Version == "" {
				continue
			}
			if version, ok := st.Workers.Versions[env.Version]; ok && version.Worker == id && version.Organization == ws.Organization {
				out.Versions[version.ID] = version
			}
		}
	}

	for _, deployment := range st.Workers.Deployments {
		if deployment.Committed == nil || !deployment.Expires.After(time.Now()) {
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
			out.Routes = append(out.Routes, domain.WorkerConfigRoute{Worker: worker.Worker, Host: host, Path: "/", Expires: &deployment.Expires, Enabled: env.Enabled, Version: version.ID, Pool: placement.Pool})
		}
		out.Versions[version.ID] = version
	}
	sort.Slice(out.Routes, func(i, j int) bool { return out.Routes[i].Host < out.Routes[j].Host })
	return out
}

func WorkerConfigOpsForPool(in []domain.WorkerConfigOp, pool string) []domain.WorkerConfigOp {
	out := make([]domain.WorkerConfigOp, 0, len(in))
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

// workerConfigDigest is the identity of a projection. A matching digest means
// the publisher has nothing new for the runtime and must not create a revision.
func WorkerConfigDigest(p domain.WorkerConfigProjection) (string, []byte, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", nil, err
	}
	return workerartifact.BytesDigest(raw), raw, nil
}

func SameWorkerConfigRoute(a, b domain.WorkerConfigRoute) bool { return a == b }

// diffWorkerConfig emits the ops needed to move a runtime from prev to next.
// Version descriptors referenced by changed routes are always included, so a
// runtime that pruned an unreferenced version can still apply a rollback.
func DiffWorkerConfig(prev *domain.WorkerConfigProjection, next domain.WorkerConfigProjection) []domain.WorkerConfigOp {
	prevRoutes := map[string]domain.WorkerConfigRoute{}
	prevVersions := map[string]domain.WorkerVersion{}
	if prev != nil {
		for _, route := range prev.Routes {
			prevRoutes[route.Host] = route
		}
		for id, version := range prev.Versions {
			prevVersions[id] = version
		}
	}
	nextRoutes := map[string]domain.WorkerConfigRoute{}
	for _, route := range next.Routes {
		nextRoutes[route.Host] = route
	}
	changedRoutes := []domain.WorkerConfigRoute{}
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
		if route.Version != "" {
			addVersion(route.Pool, route.Version)
		}
	}
	for id, version := range next.Versions {
		if old, ok := prevVersions[id]; !ok || !reflect.DeepEqual(old, version) {
			for _, route := range next.Routes {
				if route.Version == id {
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
	ops := make([]domain.WorkerConfigOp, 0, len(versionKeys)+len(changedRoutes)+len(deleted))
	for _, key := range versionKeys {
		item := neededVersions[key]
		version := item.version
		ops = append(ops, domain.WorkerConfigOp{Type: "version_put", Pool: item.pool, Version: &version})
	}
	for i := range changedRoutes {
		route := changedRoutes[i]
		ops = append(ops, domain.WorkerConfigOp{Type: "route_upsert", Pool: route.Pool, Host: route.Host, Route: &route})
	}
	for _, host := range deleted {
		ops = append(ops, domain.WorkerConfigOp{Type: "route_delete", Pool: prevRoutes[host].Pool, Host: host})
	}
	return ops
}

func EncodeWorkerConfigSnapshot(pool string, revision int64, p domain.WorkerConfigProjection) ([]byte, error) {
	snapshot := WorkerConfigSnapshot{ConfigRevision: revision, Pool: pool, Routes: p.Routes, Versions: p.Versions}
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

type WorkerConfigLatest struct {
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
	if w.Service.Store.Persistent() {
		return w.PublishConfigDelta(ctx)
	}
	// OpenStore requires PostgreSQL. This adapter is only used by memory fixtures.
	s := w.Service.Store
	var next domain.WorkerConfigProjection
	if err := s.View(ctx, storage.PartitionAll, func(st *domain.State) error { next = WorkerConfigFromState(st, w.Config); return nil }); err != nil {
		return 0, err
	}
	for attempt := 0; attempt < 8; attempt++ {
		previous, found, err := s.LatestConfigRevision(ctx)
		if err != nil {
			return 0, err
		}
		var baseline *domain.WorkerConfigProjection
		if found {
			baseline = &domain.WorkerConfigProjection{}
			if err = json.Unmarshal(previous.Payload, baseline); err != nil {
				return 0, err
			}
		}
		ops := DiffWorkerConfig(baseline, next)
		if len(ops) == 0 {
			return previous.Revision, nil
		}
		digest, payload, err := WorkerConfigDigest(next)
		if err != nil {
			return 0, err
		}
		raw, err := json.Marshal(ops)
		if err != nil {
			return 0, err
		}
		revision, _, won, err := s.AppendConfigRevisionCAS(ctx, previous.Revision, digest, payload, raw, storage.WorkerConfigOpsRetain)
		if err != nil {
			return 0, err
		}
		if won {
			s.SignalConfig()
			return revision, nil
		}
	}
	return 0, errors.New("edge config publisher CAS contention")
}

func (w *Controller) WriteConfigLatest(ctx context.Context, key string, next WorkerConfigLatest) error {
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
			var current WorkerConfigLatest
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
