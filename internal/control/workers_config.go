package control

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/minio/minio-go/v7"
)

// Edge configuration is intentionally small: routes and the immutable release
// descriptors they reference. Build logs, operations and IAM state never enter
// this stream.
type workerConfigRoute struct {
	Service string `json:"service_id,omitempty"`
	Host    string `json:"host"`
	Path    string `json:"path,omitempty"`
	Enabled bool   `json:"enabled"`
	Release string `json:"release"`
	Pool    string `json:"pool"`
}

type workerConfigProjection struct {
	Routes   []workerConfigRoute      `json:"routes"`
	Releases map[string]WorkerRelease `json:"releases"`
}

type workerConfigSnapshot struct {
	ConfigRevision int64                    `json:"config_revision"`
	Pool           string                   `json:"pool"`
	Routes         []workerConfigRoute      `json:"routes"`
	Releases       map[string]WorkerRelease `json:"releases"`
}

type workerConfigOp struct {
	Type    string             `json:"type"`
	Pool    string             `json:"pool"`
	Host    string             `json:"host,omitempty"`
	Route   *workerConfigRoute `json:"route,omitempty"`
	Release *WorkerRelease     `json:"release,omitempty"`
}

type workerConfigRevision struct {
	Revision int64
	Digest   string
	Payload  []byte
	Ops      []byte
	Created  time.Time
}

const (
	workerConfigOpsRetain = 512
	workerConfigPoolsDir  = "config/pools/"
)

func workerPoolConfigPrefix(pool string) string { return workerConfigPoolsDir + pool + "/" }
func workerPoolConfigLatestKey(pool string) string {
	return workerPoolConfigPrefix(pool) + "latest.json"
}

// workerConfigFromState includes the baseline and preview environments of every
// live service. Disabled environments stay in the projection so the runtime can
// answer 410 instead of 404; unpublished environments carry an empty release.
//
// Hosts are not stored, so they are derived here from the configured domain
// catalog and the service's selection. This is the same derivation the
// HTTPRoute reconciler and the API use, which keeps the three from drifting.
func workerConfigFromState(st *State, config *WorkersConfig) workerConfigProjection {
	out := workerConfigProjection{Routes: []workerConfigRoute{}, Releases: map[string]WorkerRelease{}}
	if st == nil || st.Workers == nil {
		return out
	}
	placements := map[string]WorkerPlacement{}
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
		ws = config.normalizeServiceNetwork(ws)
		placement, placed := placements[id]
		if !placed || placement.NetworkProfile != ws.NetworkProfile || !config.poolAllowedForProfile(ws.NetworkProfile, placement.Pool) {
			continue
		}
		pool := placement.Pool
		domains := append([]WorkerDomain(nil), config.Domains...)
		for i := range domains {
			domains[i].Domain = config.poolHostSuffix(pool, domains[i].Domain)
		}
		hosts := serviceHostPatterns(ws, domains)
		for name, env := range ws.Environments {
			// The map key is authoritative: Environment.Name is a denormalised
			// copy; the key defines the environment identity.
			if name == "" {
				continue
			}
			// One route per selected domain. The runtime matches purely on Host;
			// the Gateway instance already decided which entry a request used.
			for _, host := range hosts {
				if expanded := envHostName(host, name); expanded != "" {
					out.Routes = append(out.Routes, workerConfigRoute{Service: id, Host: expanded, Path: "/", Enabled: env.Enabled, Release: env.Release, Pool: pool})
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

func workerConfigOpsForPool(in []workerConfigOp, pool string) []workerConfigOp {
	out := make([]workerConfigOp, 0, len(in))
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
func serviceHostPatterns(ws WorkerService, domains []WorkerDomain) []string {
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
	slug := workerSlug(ws.Organization, ws.Service)
	out := []string{}
	for _, d := range domains {
		if selected[d.ID] {
			out = append(out, slug+"\x00"+d.Domain)
		}
	}
	return out
}

func envHostName(pattern, env string) string {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == 0 {
			return hostFor(pattern[:i], env, pattern[i+1:])
		}
	}
	return ""
}

// workerConfigDigest is the identity of a projection. A matching digest means
// the publisher has nothing new for the runtime and must not create a revision.
func workerConfigDigest(p workerConfigProjection) (string, []byte, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", nil, err
	}
	return bytesDigest(raw), raw, nil
}

func sameWorkerConfigRoute(a, b workerConfigRoute) bool { return a == b }

// diffWorkerConfig emits the ops needed to move a runtime from prev to next.
// Release descriptors referenced by changed routes are always included, so a
// runtime that pruned an unreferenced release can still apply a rollback.
func diffWorkerConfig(prev *workerConfigProjection, next workerConfigProjection) []workerConfigOp {
	prevRoutes := map[string]workerConfigRoute{}
	prevReleases := map[string]WorkerRelease{}
	if prev != nil {
		for _, route := range prev.Routes {
			prevRoutes[route.Host] = route
		}
		for id, release := range prev.Releases {
			prevReleases[id] = release
		}
	}
	nextRoutes := map[string]workerConfigRoute{}
	for _, route := range next.Routes {
		nextRoutes[route.Host] = route
	}
	changedRoutes := []workerConfigRoute{}
	type pooledRelease struct {
		pool    string
		release WorkerRelease
	}
	neededReleases := map[string]pooledRelease{}
	addRelease := func(pool, id string) {
		if release, ok := next.Releases[id]; ok {
			neededReleases[pool+"\x00"+id] = pooledRelease{pool: pool, release: release}
		}
	}
	for _, route := range next.Routes {
		old, ok := prevRoutes[route.Host]
		if ok && sameWorkerConfigRoute(old, route) {
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
	ops := make([]workerConfigOp, 0, len(releaseKeys)+len(changedRoutes)+len(deleted))
	for _, key := range releaseKeys {
		item := neededReleases[key]
		release := item.release
		ops = append(ops, workerConfigOp{Type: "release_put", Pool: item.pool, Release: &release})
	}
	for i := range changedRoutes {
		route := changedRoutes[i]
		ops = append(ops, workerConfigOp{Type: "route_upsert", Pool: route.Pool, Host: route.Host, Route: &route})
	}
	for _, host := range deleted {
		ops = append(ops, workerConfigOp{Type: "route_delete", Pool: prevRoutes[host].Pool, Host: host})
	}
	return ops
}

func encodeWorkerConfigSnapshot(pool string, revision int64, p workerConfigProjection) ([]byte, error) {
	snapshot := workerConfigSnapshot{ConfigRevision: revision, Pool: pool, Routes: p.Routes, Releases: p.Releases}
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

type workerConfigLatest struct {
	LazyRoutes     bool   `json:"lazy_routes,omitempty"`
	ConfigRevision int64  `json:"config_revision"`
	Snapshot       string `json:"snapshot"`
	SHA256         string `json:"sha256"`
	OpsFloor       int64  `json:"ops_floor"`
}

// publishConfig serializes local publication; PostgreSQL coordinates replicas
// through the per-service dirty queue and revision CAS.
func (w *workersController) publishConfig(ctx context.Context) (int64, error) {
	w.configMu.Lock()
	defer w.configMu.Unlock()
	if w.server.Store.pool != nil {
		return w.publishConfigDelta(ctx)
	}
	// OpenStore requires PostgreSQL. This adapter is only used by memory fixtures.
	s := w.server.Store
	var next workerConfigProjection
	if err := s.View(ctx, PartitionAll, func(st *State) error { next = workerConfigFromState(st, w.config); return nil }); err != nil {
		return 0, err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	var previous *workerConfigProjection
	if len(s.configRevs) > 0 {
		previous = &workerConfigProjection{}
		if err := json.Unmarshal(s.configRevs[len(s.configRevs)-1].Payload, previous); err != nil {
			return 0, err
		}
	}
	ops := diffWorkerConfig(previous, next)
	if len(ops) == 0 {
		return s.configSeq, nil
	}
	digest, payload, err := workerConfigDigest(next)
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(ops)
	if err != nil {
		return 0, err
	}
	s.configSeq++
	s.configRevs = append(s.configRevs, workerConfigRevision{Revision: s.configSeq, Digest: digest, Payload: payload, Ops: raw, Created: now()})
	if len(s.configRevs) > workerConfigOpsRetain {
		s.configRevs = append([]workerConfigRevision(nil), s.configRevs[len(s.configRevs)-workerConfigOpsRetain:]...)
	}
	if s.configCh != nil {
		close(s.configCh)
	}
	s.configCh = make(chan struct{})
	return s.configSeq, nil
}

func (w *workersController) writeConfigLatest(ctx context.Context, key string, next workerConfigLatest) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if w.s3 == nil {
		return w.putBytes(ctx, key, raw, minio.PutObjectOptions{ContentType: "application/json", CacheControl: "no-store"})
	}
	// The pointer is the only mutable object. Use the object's ETag as a second
	// CAS so an older publisher cannot overwrite a newer revision between GET
	// and PUT. A precondition race is retried after observing the winner.
	for attempts := 0; attempts < 4; attempts++ {
		stat, statErr := w.s3.StatObject(ctx, w.config.S3Bucket, key, minio.StatObjectOptions{})
		opts := minio.PutObjectOptions{ContentType: "application/json", CacheControl: "no-store"}
		if statErr == nil {
			currentRaw, readErr := w.getObject(ctx, key)
			if readErr != nil {
				return readErr
			}
			var current workerConfigLatest
			if json.Unmarshal(currentRaw, &current) == nil {
				if current.ConfigRevision > next.ConfigRevision {
					return nil
				}
				if current.ConfigRevision == next.ConfigRevision && current.Snapshot == next.Snapshot && current.SHA256 == next.SHA256 && current.OpsFloor == next.OpsFloor {
					return nil
				}
			}
			opts.SetMatchETag(stat.ETag)
		} else if code := minio.ToErrorResponse(statErr).Code; code == "NoSuchKey" || code == "NoSuchObject" || code == "NotFound" {
			opts.SetMatchETagExcept("*")
		} else {
			return statErr
		}
		if err := w.putBytes(ctx, key, raw, opts); err == nil {
			return nil
		} else if minio.ToErrorResponse(err).Code != "PreconditionFailed" {
			return err
		}
	}
	return errors.New("edge config pointer changed repeatedly")
}
