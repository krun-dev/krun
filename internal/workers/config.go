package workers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

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
	Releases       map[string]domain.WorkerRelease `json:"releases"`
}

const (
	WorkerConfigPoolsDir = "config/pools/"
)

func WorkerPoolConfigPrefix(pool string) string { return WorkerConfigPoolsDir + pool + "/" }

func WorkerPoolConfigLatestKey(pool string) string {
	return WorkerPoolConfigPrefix(pool) + "latest.json"
}

// workerConfigFromState includes the baseline and preview environments of every
// live service. Disabled environments stay in the projection so the runtime can
// answer 410 instead of 404; unpublished environments carry an empty release.
//
// Hosts are not stored, so they are derived here from the configured domain
// catalog and the service's selection. This is the same derivation the
// HTTPRoute reconciler and the API use, which keeps the three from drifting.
func WorkerConfigFromState(st *domain.State, config *appconfig.WorkersConfig) domain.WorkerConfigProjection {
	out := domain.WorkerConfigProjection{Routes: []domain.WorkerConfigRoute{}, Releases: map[string]domain.WorkerRelease{}}
	if st == nil || st.Workers == nil {
		return out
	}
	placements := map[string]domain.WorkerPlacement{}
	for _, p := range st.Workers.Placements {
		if p.Role == "primary" && p.State == "active" {
			placements[p.Service] = p
		}
	}
	for id, ws := range st.Workers.Services {
		resource, ok := st.Resources[id]
		if !ok || resource.Kind != "service" || resource.Organization != ws.Organization {
			continue
		}
		if _, ok := st.Organizations[ws.Organization]; !ok {
			continue
		}
		ws = config.NormalizeServiceNetwork(ws)
		placement, placed := placements[id]
		if !placed || placement.NetworkProfile != ws.NetworkProfile || !config.PoolAllowedForProfile(ws.NetworkProfile, placement.Pool) {
			continue
		}
		pool := placement.Pool
		domains := append([]appconfig.WorkerDomain(nil), config.Domains...)
		for i := range domains {
			domains[i].Domain = config.PoolHostSuffix(pool, domains[i].Domain)
		}
		hosts := ServiceHostPatterns(ws, domains)
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
					out.Routes = append(out.Routes, domain.WorkerConfigRoute{Service: id, Host: expanded, Path: "/", Enabled: env.Enabled, Release: env.Release, Pool: pool})
				}
			}
			if env.Release == "" {
				continue
			}
			if release, ok := st.Workers.Releases[env.Release]; ok && release.Service == id && release.Organization == ws.Organization {
				out.Releases[release.ID] = release
			}
		}
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

// serviceHostPatterns returns one "<slug>\x00<suffix>" pattern per selected
// domain, in catalog order. envHostName expands the environment segment. A
// service with no stored selection falls back to the catalog defaults, matching
// what normalizeService does for the API and the route reconciler.
func ServiceHostPatterns(ws domain.WorkerService, domains []appconfig.WorkerDomain) []string {
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
	slug := appconfig.WorkerSlug(ws.Organization, ws.Service)
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
// Release descriptors referenced by changed routes are always included, so a
// runtime that pruned an unreferenced release can still apply a rollback.
func DiffWorkerConfig(prev *domain.WorkerConfigProjection, next domain.WorkerConfigProjection) []domain.WorkerConfigOp {
	prevRoutes := map[string]domain.WorkerConfigRoute{}
	prevReleases := map[string]domain.WorkerRelease{}
	if prev != nil {
		for _, route := range prev.Routes {
			prevRoutes[route.Host] = route
		}
		for id, release := range prev.Releases {
			prevReleases[id] = release
		}
	}
	nextRoutes := map[string]domain.WorkerConfigRoute{}
	for _, route := range next.Routes {
		nextRoutes[route.Host] = route
	}
	changedRoutes := []domain.WorkerConfigRoute{}
	type PooledRelease struct {
		pool    string
		release domain.WorkerRelease
	}
	neededReleases := map[string]PooledRelease{}
	addRelease := func(pool, id string) {
		if release, ok := next.Releases[id]; ok {
			neededReleases[pool+"\x00"+id] = PooledRelease{pool: pool, release: release}
		}
	}
	for _, route := range next.Routes {
		old, ok := prevRoutes[route.Host]
		if ok && SameWorkerConfigRoute(old, route) {
			continue
		}
		changedRoutes = append(changedRoutes, route)
		if route.Release != "" {
			addRelease(route.Pool, route.Release)
		}
	}
	for id, release := range next.Releases {
		if old, ok := prevReleases[id]; !ok || !reflect.DeepEqual(old, release) {
			for _, route := range next.Routes {
				if route.Release == id {
					addRelease(route.Pool, id)
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
	releaseKeys := make([]string, 0, len(neededReleases))
	for key := range neededReleases {
		releaseKeys = append(releaseKeys, key)
	}
	sort.Strings(releaseKeys)
	sort.Slice(changedRoutes, func(i, j int) bool { return changedRoutes[i].Host < changedRoutes[j].Host })
	ops := make([]domain.WorkerConfigOp, 0, len(releaseKeys)+len(changedRoutes)+len(deleted))
	for _, key := range releaseKeys {
		item := neededReleases[key]
		release := item.release
		ops = append(ops, domain.WorkerConfigOp{Type: "release_put", Pool: item.pool, Release: &release})
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
	snapshot := WorkerConfigSnapshot{ConfigRevision: revision, Pool: pool, Routes: p.Routes, Releases: p.Releases}
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
// through the per-service dirty queue and revision CAS.
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
