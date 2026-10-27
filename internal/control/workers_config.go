package control

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/minio/minio-go/v7"
)

// Edge configuration is intentionally small: routes and the immutable release
// descriptors they reference. Build logs, operations and IAM state never enter
// this stream.
type workerConfigRoute struct {
	Host    string `json:"host"`
	Path    string `json:"path,omitempty"`
	Enabled bool   `json:"enabled"`
	Release string `json:"release"`
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
	workerConfigOpsRetain   = 512
	workerConfigLatestKey   = "config/latest.json"
	workerConfigSnapshotDir = "config/snapshots/"
)

func workerConfigSnapshotKey(revision int64) string {
	return workerConfigSnapshotDir + strconv.FormatInt(revision, 10) + ".json.gz"
}

// The pointer stores a path relative to config_base_url (which already ends in
// /config), while the object key includes that prefix for bucket layout.
func workerConfigSnapshotRel(revision int64) string {
	return "snapshots/" + strconv.FormatInt(revision, 10) + ".json.gz"
}

// workerConfigFromState includes the baseline and preview environments of every
// live service. Disabled environments stay in the projection so the runtime can
// answer 410 instead of 404; unpublished environments carry an empty release.
//
// Hosts are not stored, so they are derived here from the configured domain
// catalog and the service's selection. This is the same derivation the
// HTTPRoute reconciler and the API use, which keeps the three from drifting.
func workerConfigFromState(st *State, domains []WorkerDomain) workerConfigProjection {
	out := workerConfigProjection{Routes: []workerConfigRoute{}, Releases: map[string]WorkerRelease{}}
	if st == nil || st.Workers == nil {
		return out
	}
	for id, ws := range st.Workers.Services {
		resource, ok := st.Resources[id]
		if !ok || resource.Kind != "service" || resource.Organization != ws.Organization {
			continue
		}
		if _, ok := st.Organizations[ws.Organization]; !ok {
			continue
		}
		hosts := serviceHostPatterns(ws, domains)
		for name, env := range ws.Environments {
			// The map key is authoritative: Environment.Name is a denormalised
			// copy that older rows may not carry.
			if name == "" {
				continue
			}
			// One route per selected domain. The runtime matches purely on Host;
			// the Gateway instance already decided which entry a request used.
			for _, host := range hosts {
				if expanded := envHostName(host, name); expanded != "" {
					out.Routes = append(out.Routes, workerConfigRoute{Host: expanded, Path: "/", Enabled: env.Enabled, Release: env.Release})
				}
			}
			if env.Release == "" {
				continue
			}
			if release, ok := st.Workers.Releases[env.Release]; ok && release.Pool == "default" && release.Service == id && release.Organization == ws.Organization {
				out.Releases[release.ID] = release
			}
		}
	}
	sort.Slice(out.Routes, func(i, j int) bool { return out.Routes[i].Host < out.Routes[j].Host })
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
	neededReleases := map[string]WorkerRelease{}
	for _, route := range next.Routes {
		old, ok := prevRoutes[route.Host]
		if ok && sameWorkerConfigRoute(old, route) {
			continue
		}
		changedRoutes = append(changedRoutes, route)
		if route.Release != "" {
			if release, ok := next.Releases[route.Release]; ok {
				neededReleases[release.ID] = release
			}
		}
	}
	for id, release := range next.Releases {
		if old, ok := prevReleases[id]; !ok || !reflect.DeepEqual(old, release) {
			neededReleases[id] = release
		}
	}
	deleted := []string{}
	for host := range prevRoutes {
		if _, ok := nextRoutes[host]; !ok {
			deleted = append(deleted, host)
		}
	}
	sort.Strings(deleted)
	releaseIDs := make([]string, 0, len(neededReleases))
	for id := range neededReleases {
		releaseIDs = append(releaseIDs, id)
	}
	sort.Strings(releaseIDs)
	sort.Slice(changedRoutes, func(i, j int) bool { return changedRoutes[i].Host < changedRoutes[j].Host })
	ops := make([]workerConfigOp, 0, len(releaseIDs)+len(changedRoutes)+len(deleted))
	for _, id := range releaseIDs {
		release := neededReleases[id]
		ops = append(ops, workerConfigOp{Type: "release_put", Release: &release})
	}
	for i := range changedRoutes {
		route := changedRoutes[i]
		ops = append(ops, workerConfigOp{Type: "route_upsert", Host: route.Host, Route: &route})
	}
	for _, host := range deleted {
		ops = append(ops, workerConfigOp{Type: "route_delete", Host: host})
	}
	return ops
}

func encodeWorkerConfigSnapshot(revision int64, p workerConfigProjection) ([]byte, error) {
	snapshot := workerConfigSnapshot{ConfigRevision: revision, Pool: "default", Routes: p.Routes, Releases: p.Releases}
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
	ConfigRevision int64  `json:"config_revision"`
	Snapshot       string `json:"snapshot"`
	SHA256         string `json:"sha256"`
	OpsFloor       int64  `json:"ops_floor"`
	GeneratedAt    string `json:"generated_at"`
}

// publishConfig materialises the current edge projection into an immutable
// revision. Replicas race on the durable head CAS. A digest match is a no-op;
// a failed object-store write leaves configDirty set so the next cycle retries
// the snapshot without creating another revision.
func (w *workersController) publishConfig(ctx context.Context) (int64, error) {
	w.configMu.Lock()
	defer w.configMu.Unlock()
	if err := w.loadConfigLocked(ctx); err != nil {
		return 0, err
	}
	if w.configTopologyLoaded && !w.configDirty {
		generation, err := w.server.Store.WorkerTopologyGeneration(ctx)
		if err != nil {
			return 0, err
		}
		if generation >= 0 && generation == w.configTopologyGeneration {
			return w.configRevision, nil
		}
	}
	next, topologyGeneration, err := w.server.Store.WorkerConfigProjection(ctx, w.config.Domains)
	if err != nil {
		return 0, err
	}
	digest, payload, err := workerConfigDigest(next)
	if err != nil {
		return 0, err
	}
	if w.configLast != nil {
		previous, _, err := workerConfigDigest(*w.configLast)
		if err != nil {
			return 0, err
		}
		if previous == digest {
			if !w.configDirty {
				w.configTopologyGeneration = topologyGeneration
				w.configTopologyLoaded = topologyGeneration >= 0
				return w.configRevision, nil
			}
			if err := w.writeConfigRevision(ctx, w.configRevision, next); err != nil {
				return w.configRevision, err
			}
			w.configDirty = false
			w.configTopologyGeneration = topologyGeneration
			w.configTopologyLoaded = topologyGeneration >= 0
			return w.configRevision, nil
		}
	}
	for attempts := 0; attempts < 8; attempts++ {
		ops := diffWorkerConfig(w.configLast, next)
		opsRaw, marshalErr := json.Marshal(ops)
		if marshalErr != nil {
			return 0, marshalErr
		}
		revision, floor, appended, appendErr := w.server.Store.AppendConfigRevisionCAS(ctx, w.configRevision, digest, payload, opsRaw, workerConfigOpsRetain)
		if appendErr != nil {
			return 0, appendErr
		}
		if appended {
			w.configRevision = revision
			w.configFloor = floor
			w.configLast = &next
			w.configDirty = true
			w.server.Store.SignalConfig()
			if err := w.writeConfigRevision(ctx, revision, next); err != nil {
				return revision, err
			}
			w.configDirty = false
			w.configTopologyGeneration = topologyGeneration
			w.configTopologyLoaded = topologyGeneration >= 0
			return revision, nil
		}

		// Another replica advanced the head. A winner with the same digest is
		// sufficient for this caller; a different digest is not. Adopt and repair
		// the winner, then reload current DB state and CAS the desired projection
		// from that head in this call. Returning the stale winner would let an
		// operation accept its ack before its own release was published.
		latest, ok, loadErr := w.server.Store.LatestConfigRevision(ctx)
		if loadErr != nil {
			return 0, loadErr
		}
		if !ok || latest.Revision != revision {
			w.configLoaded = false
			return revision, errors.New("edge config head changed without a matching revision")
		}
		won, decodeErr := decodePublishedWorkerConfig(latest.Payload)
		if decodeErr != nil {
			w.configLoaded = false
			return revision, decodeErr
		}
		winnerDigest, _, digestErr := workerConfigDigest(won)
		if digestErr != nil {
			w.configLoaded = false
			return revision, digestErr
		}
		if winnerDigest != latest.Digest {
			w.configLoaded = false
			return revision, errors.New("edge config head digest does not match normalized projection")
		}
		w.configRevision = latest.Revision
		w.configFloor = floor
		w.configLast = &won
		w.configDirty = true
		if err := w.writeConfigRevision(ctx, latest.Revision, won); err != nil {
			return latest.Revision, err
		}
		w.configDirty = false
		if latest.Digest == digest {
			w.configTopologyGeneration = topologyGeneration
			w.configTopologyLoaded = topologyGeneration >= 0
			return latest.Revision, nil
		}
		next, topologyGeneration, err = w.server.Store.WorkerConfigProjection(ctx, w.config.Domains)
		if err != nil {
			return 0, err
		}
		digest, payload, err = workerConfigDigest(next)
		if err != nil {
			return 0, err
		}
		if digest == latest.Digest {
			w.configTopologyGeneration = topologyGeneration
			w.configTopologyLoaded = topologyGeneration >= 0
			return latest.Revision, nil
		}
	}
	return w.configRevision, errors.New("edge config head changed repeatedly")
}

func (w *workersController) loadConfigLocked(ctx context.Context) error {
	if w.configLoaded {
		return nil
	}
	latest, ok, err := w.server.Store.LatestConfigRevision(ctx)
	if err != nil {
		return err
	}
	if !ok {
		w.configLoaded = true
		return nil
	}
	projection, err := decodePublishedWorkerConfig(latest.Payload)
	if err != nil {
		// Fail closed. Diffing from nil would emit only upserts and could leave a
		// deleted route alive on runtimes that already applied the prior head.
		return err
	}
	digest, _, err := workerConfigDigest(projection)
	if err != nil {
		return err
	}
	if digest != latest.Digest {
		return errors.New("edge config head digest does not match normalized projection")
	}
	w.configLoaded = true
	w.configRevision = latest.Revision
	w.configLast = &projection
	if _, floor, err := w.server.Store.ConfigState(ctx); err == nil {
		w.configFloor = floor
	}
	// Repair a missing or stale object-store snapshot after a control-plane
	// restart without creating a new revision.
	w.configDirty = true
	return nil
}

func (w *workersController) writeConfigRevision(ctx context.Context, revision int64, projection workerConfigProjection) error {
	// The config prefix lives in the same bucket as artifacts. Create the bucket
	// and its anonymous-read policy before the first publish so a fresh cluster
	// can bootstrap without waiting for a build.
	if w.s3 != nil && !w.configBucket {
		if err := w.ensureBucket(ctx); err != nil {
			return err
		}
		w.configBucket = true
	}
	raw, err := encodeWorkerConfigSnapshot(revision, projection)
	if err != nil {
		return err
	}
	key := workerConfigSnapshotKey(revision)
	if w.s3 != nil {
		if err := w.putImmutable(ctx, key, raw); err != nil {
			return err
		}
	} else if err := w.putBytes(ctx, key, raw, minio.PutObjectOptions{ContentType: "application/gzip", CacheControl: "no-store"}); err != nil {
		return err
	}
	return w.writeConfigLatest(ctx, workerConfigLatest{ConfigRevision: revision, Snapshot: workerConfigSnapshotRel(revision), SHA256: bytesDigest(raw), OpsFloor: w.configFloor, GeneratedAt: now().UTC().Format(time.RFC3339)})
}

func (w *workersController) writeConfigLatest(ctx context.Context, next workerConfigLatest) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if w.s3 == nil {
		return w.putBytes(ctx, workerConfigLatestKey, raw, minio.PutObjectOptions{ContentType: "application/json", CacheControl: "no-store"})
	}
	// The pointer is the only mutable object. Use the object's ETag as a second
	// CAS so an older publisher cannot overwrite a newer revision between GET
	// and PUT. A precondition race is retried after observing the winner.
	for attempts := 0; attempts < 4; attempts++ {
		stat, statErr := w.s3.StatObject(ctx, w.config.S3Bucket, workerConfigLatestKey, minio.StatObjectOptions{})
		opts := minio.PutObjectOptions{ContentType: "application/json", CacheControl: "no-store"}
		if statErr == nil {
			currentRaw, readErr := w.getObject(ctx, workerConfigLatestKey)
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
		if err := w.putBytes(ctx, workerConfigLatestKey, raw, opts); err == nil {
			return nil
		} else if minio.ToErrorResponse(err).Code != "PreconditionFailed" {
			return err
		}
	}
	return errors.New("edge config pointer changed repeatedly")
}
