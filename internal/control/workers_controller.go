package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
)

// workerPutObject is the narrow object-write seam used by source bundles and
// per-file asset copies. Production wires it to the S3 client; tests inject it.
type workerPutObject func(ctx context.Context, bucket, object string, reader io.Reader, size int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)

type workersController struct {
	server    *Server
	config    *WorkersConfig
	s3        *minio.Client
	putObject workerPutObject
	// Builder and runtime URLs may use different network views of the same
	// immutable bucket. Presigning is offline; the control process need not
	// resolve either endpoint.
	s3Builder *minio.Client
	s3Runtime *minio.Client
	kube      *workerKubernetes
	http      *http.Client
	// Parsed asset manifests are immutable per artifact; cache them so presigning
	// a single file does not re-download the archive.
	assetMu    sync.RWMutex
	assetCache map[string]workerCachedAssets
	// Edge config publication is coordinated by a durable revision CAS; every
	// replica may race without electing a process-wide leader.
	configMu       sync.Mutex
	configLoaded   bool
	configDirty    bool
	configBucket   bool
	configLast     *workerConfigProjection
	configRevision int64
	configFloor    int64
	// configTopologyGeneration gates the expensive edge read model. PostgreSQL
	// mutations advance the durable generation in the same transaction as a
	// topology change, so idle reconcile cycles need only read one meta row.
	configTopologyGeneration  int64
	configTopologyLoaded      bool
	routingTopology           map[string]WorkerService
	routingTopologyGeneration int64
	routingTopologyLoaded     bool
	lastGC                    time.Time
	// Local build executor state for builder_mode: local.
	localMu     sync.Mutex
	localBuilds map[string]*workerLocalBuild
	localWG     sync.WaitGroup
	lifecycle   context.Context
	// replica identifies this process in claimed_by columns. It only needs to be
	// unique among live replicas, not stable across restarts.
	replica string
}

type workerLocalBuild struct{ Running bool }

type workerCachedAssets struct {
	Config *WorkerAssetsConfig
	Files  []workerManifestFile
}

func newWorkersController(s *Server) (*workersController, error) {
	s3, e := newWorkerS3(s.Config.Workers)
	if e != nil {
		return nil, e
	}
	s3Builder := s3
	if s.Config.Workers.S3BuilderEndpoint != s.Config.Workers.S3Endpoint {
		s3Builder, e = newWorkerS3Endpoint(s.Config.Workers, s.Config.Workers.S3BuilderEndpoint)
		if e != nil {
			return nil, e
		}
	}
	s3Runtime := s3Builder
	if s.Config.Workers.S3RuntimeEndpoint != s.Config.Workers.S3BuilderEndpoint {
		s3Runtime, e = newWorkerS3Endpoint(s.Config.Workers, s.Config.Workers.S3RuntimeEndpoint)
		if e != nil {
			return nil, e
		}
	}
	k, e := newWorkerKubernetes(s.Config.Workers)
	if e != nil {
		return nil, e
	}
	return &workersController{server: s, config: s.Config.Workers, s3: s3, putObject: s3.PutObject, s3Builder: s3Builder, s3Runtime: s3Runtime, kube: k, http: &http.Client{Timeout: 8 * time.Second}, assetCache: map[string]workerCachedAssets{}, localBuilds: map[string]*workerLocalBuild{}, replica: workerReplicaID()}, nil
}

// workerReplicaID names this process in claimed_by columns.
func workerReplicaID() string {
	host, _ := os.Hostname()
	return host + "/" + strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
}

// authorizeActor re-checks a durable operation's actor against current IAM
// state. Authorization needs both partitions: the grants and user rows are IAM,
// but resolve() looks the service up in krun_resources, which is Workers.
func (w *workersController) authorizeActor(ctx context.Context, actor WorkerActor, service, env, action string) (error, error) {
	var denied error
	err := w.server.Store.View(ctx, PartitionAll, func(st *State) error {
		denied = w.server.workerAuthorizeActor(st, actor, service, env, action)
		return nil
	})
	return denied, err
}

// putBytes writes a whole object through the injected storage hook.
func (w *workersController) putBytes(ctx context.Context, key string, data []byte, opts minio.PutObjectOptions) error {
	if w.putObject == nil {
		return errors.New("object storage unavailable")
	}
	_, e := w.putObject(ctx, w.config.S3Bucket, key, bytes.NewReader(data), int64(len(data)), opts)
	return e
}

func (w *workersController) run(ctx context.Context) {
	w.lifecycle = ctx
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.localWG.Wait()
			return
		case <-ticker.C:
			if w.server.Store.pool == nil {
				continue
			}
			// No leader election: each cycle claims individual operations and
			// builds with a conditional update, so replicas work on different
			// rows instead of queueing behind one advisory lock.
			cycle, cancel := context.WithTimeout(ctx, 25*time.Second)
			e := w.reconcile(cycle)
			cancel()
			if e != nil && ctx.Err() == nil {
				fmt.Println("workers reconcile:", e)
			}
		}
	}
}

// reconcile runs one cycle. It reads only the routing topology plus unfinished
// work, so an idle control plane scans no build or operation rows and never
// deserializes a whole document.
func (w *workersController) reconcile(ctx context.Context) error {
	var failures []error
	// Materialise an edge-config revision whenever routes or releases changed.
	// A digest match is a no-op, so this is safe to run every cycle.
	if _, e := w.publishConfig(ctx); e != nil {
		failures = append(failures, e)
	}
	if w.lastGC.IsZero() || now().Sub(w.lastGC) >= time.Minute {
		// Rate-limit even failed attempts; an object-store outage must not turn
		// the one-second reconcile loop into a tight remote retry loop.
		w.lastGC = now()
		if e := w.collectWorkerObjects(ctx); e != nil {
			failures = append(failures, e)
		}
	}
	// The route reconciler repairs Kubernetes every cycle, but its desired
	// topology changes far less often. Keep the compact read model behind the
	// same durable generation used by config publication.
	topology := w.routingTopology
	generation, e := w.server.Store.WorkerTopologyGeneration(ctx)
	if e != nil {
		return e
	}
	if !w.routingTopologyLoaded || generation < 0 || generation != w.routingTopologyGeneration {
		topology, generation, e = w.server.Store.WorkerRoutingTopology(ctx)
		if e != nil {
			return e
		}
		w.routingTopology = topology
		w.routingTopologyGeneration = generation
		w.routingTopologyLoaded = generation >= 0
	}
	pending, e := w.server.Store.pendingWork(ctx, 100)
	if e != nil {
		return e
	}
	builds := make([]WorkerBuild, 0, len(pending.Builds))
	for _, b := range pending.Builds {
		builds = append(builds, b)
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].Created.Before(builds[j].Created) })
	for _, b := range builds {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		claimed, claimErr := w.server.Store.claimBuild(ctx, b.ID, w.replica)
		if claimErr != nil {
			failures = append(failures, claimErr)
			continue
		}
		if !claimed {
			continue
		}
		b.State = "building"
		if e = w.processClaimedBuild(ctx, b, pending.BuildPrivate[b.ID]); e != nil {
			failures = append(failures, e)
		}
	}
	// One shared static route per domain; it does not scale with services.
	// Each route lives on the Gateway instance that domain is bound to. Assets
	// are written to both the public and internal object prefixes. The explicit
	// portable access class, rather than catalog order or a private pool name,
	// chooses which prefix a domain serves.
	desiredStatic := map[string]bool{}
	for _, d := range w.config.Domains {
		desiredStatic["workers-static-"+d.ID] = true
		prefix := "int"
		if d.Access == "public" {
			prefix = "pub"
		}
		if e := w.kube.ensureStaticRoute(ctx, w.config, d.ID, prefix); e != nil {
			failures = append(failures, e)
		}
	}
	// Static routes from a previous catalog (renamed or removed domains) would
	// keep serving objects on hosts nobody configured any more.
	staleStatic, staleErr := staleWorkerRoutes(ctx, w.kube, "workers-static-", desiredStatic)
	if staleErr != nil {
		failures = append(failures, staleErr)
	} else {
		for _, n := range staleStatic {
			if e := w.kube.deleteRoute(ctx, n); e != nil {
				failures = append(failures, e)
			}
		}
	}
	// One HTTPRoute per (service, Gateway instance). Hosts are grouped by the
	// Gateway their domain is bound to, so routes never carry a hostname the
	// instance is not meant to serve.
	routesReady := map[string]bool{}
	desiredRoutes := map[string]bool{}
	for name := range desiredStatic {
		desiredRoutes[name] = true
	}
	for _, service := range topology {
		pool, ok := w.config.runtimePool(service.Pool)
		if !ok {
			failures = append(failures, fmt.Errorf("worker service %s references unknown runtime pool %q", service.Service, service.Pool))
			routesReady[service.Service] = false
			continue
		}
		hostsByGateway := map[string][]string{}
		selectedDomains := service.Domains
		if len(selectedDomains) == 0 {
			selectedDomains = w.config.defaultDomains()
		}
		for _, env := range service.Environments {
			if !env.Enabled {
				continue
			}
			// Hosts are derived from the domain catalog, never persisted, so a
			// catalog change cannot leave stale hosts behind.
			for _, id := range selectedDomains {
				d, ok := w.config.domain(id)
				if !ok {
					continue
				}
				host := hostFor(workerSlug(service.Organization, service.Service), env.Name, d.Domain)
				if host == "" {
					continue
				}
				hostsByGateway[d.Gateway] = append(hostsByGateway[d.Gateway], host)
			}
		}
		baseName := "workers-" + digest(service.Organization + "/" + service.Service)[:24]
		for i, gateway := range w.config.Gateways {
			name := baseName
			if i > 0 {
				name += "-" + gateway.ID
			}
			desiredRoutes[name] = true
		}
		ready, routeError := w.kube.ensureRoutes(ctx, w.config, baseName, pool.RuntimeService, hostsByGateway)
		routesReady[service.Service] = ready && routeError == nil
		if routeError != nil {
			failures = append(failures, routeError)
		}
	}
	// A service that disappeared from IAM is not visited above. Remove its old
	// HTTPRoutes explicitly rather than waiting for a future service write.
	staleRoutes, staleErr := staleWorkerRoutes(ctx, w.kube, "workers-", desiredRoutes)
	if staleErr != nil {
		failures = append(failures, staleErr)
	} else {
		for _, name := range staleRoutes {
			if e := w.kube.deleteRoute(ctx, name); e != nil {
				failures = append(failures, e)
			}
		}
	}
	ops := make([]WorkerOperation, 0, len(pending.Operations))
	for _, op := range pending.Operations {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Created.Before(ops[j].Created) })
	for _, op := range ops {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = w.processOperation(ctx, op, pending, routesReady[op.Service]); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

func (w *workersController) processClaimedBuild(ctx context.Context, b WorkerBuild, private WorkerBuildPrivate) error {
	workCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	leaseErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				owned, err := w.server.Store.renewBuildClaim(workCtx, b.ID, w.replica)
				if err == nil && owned {
					continue
				}
				if err == nil {
					err = errors.New("build lease lost")
				}
				select {
				case <-done:
					return
				default:
					leaseErr <- err
					cancel()
					return
				}
			}
		}
	}()
	err := w.processBuild(workCtx, b, private)
	close(done)
	cancel()
	select {
	case lost := <-leaseErr:
		if err == nil {
			return lost
		}
	default:
	}
	return err
}

func (w *workersController) failBuild(ctx context.Context, id, message, log string) error {
	return w.server.Store.finishClaimedBuild(ctx, id, w.replica, "failed", message, "", safeWorkerLog(log))
}
func (w *workersController) processBuild(ctx context.Context, b WorkerBuild, private WorkerBuildPrivate) error {
	denied, e := w.authorizeActor(ctx, private.Actor, b.Service, b.Environment, "build.start")
	if e != nil {
		return e
	}
	if denied != nil {
		return w.failBuild(ctx, b.ID, "Build permission revoked", "")
	}
	if now().Sub(b.Created) > 180*time.Second {
		log := ""
		if w.config.BuilderMode != "local" {
			log = w.kube.buildLog(ctx, b.Job)
		}
		return w.failBuild(ctx, b.ID, "Build deadline exceeded", log)
	}
	if b.Repository != nil && private.SourceKey == "" {
		var err error
		b, private, err = w.prepareRepositoryBuild(ctx, b, private)
		if err != nil {
			if errors.Is(err, errBuildClaimLost) || ctx.Err() != nil {
				return err
			}
			return w.failBuild(ctx, b.ID, err.Error(), private.Log)
		}
	}
	// Legacy inline builds still arrive as a file map; move them onto the same
	// source-bundle path so the builder never receives files through a ConfigMap.
	if private.SourceKey == "" && len(private.Files) > 0 {
		bundle, err := workerSourceBundle(private.Files, nil)
		if err != nil {
			return w.failBuild(ctx, b.ID, err.Error(), "")
		}
		if e := w.ensureBucket(ctx); e != nil {
			return e
		}
		key := workerSourceUploadKey(b.Organization, b.ID)
		if e := w.putBytes(ctx, key, bundle, minio.PutObjectOptions{ContentType: "application/gzip"}); e != nil {
			return e
		}
		private.SourceKey = key
		private.Files = nil
		if e := w.server.Store.setBuildSource(ctx, b.ID, key); e != nil {
			return e
		}
	}
	if w.config.BuilderMode == "local" {
		return w.processLocalBuild(ctx, b, private)
	}
	var job workerJob
	e = w.kube.call(ctx, "GET", w.kube.jobs()+"/"+b.Job, nil, &job)
	if kubeMissing(e) {
		if e = w.ensureBucket(ctx); e != nil {
			return e
		}
		signed, e := w.uploadURL(ctx, b)
		if e != nil {
			return e
		}
		sourceURL, e := w.sourceBundleURL(ctx, private.SourceKey)
		if e != nil {
			return e
		}
		if e = w.kube.ensureBuildJob(ctx, b, private, sourceURL, signed, w.config.BuilderImage, w.config.BuilderPullPolicy); e != nil {
			return e
		}
		return nil
	}
	if e != nil {
		return e
	}
	if job.Status.Failed > 0 {
		return w.failBuild(ctx, b.ID, "Builder failed", w.kube.buildLog(ctx, b.Job))
	}
	if job.Status.Succeeded == 0 {
		return nil
	}
	artifact, e := w.finalizeBuild(ctx, b)
	if e != nil {
		if errors.Is(e, errArtifactBeingCollected) {
			return nil
		}
		return w.failBuild(ctx, b.ID, "Artifact validation failed: "+e.Error(), w.kube.buildLog(ctx, b.Job))
	}
	log := safeWorkerLog(private.Log + w.kube.buildLog(ctx, b.Job))
	// Re-check permission immediately before the artifact becomes usable.
	denied, e = w.authorizeActor(ctx, private.Actor, b.Service, b.Environment, "build.start")
	if e != nil {
		return e
	}
	if denied != nil {
		return w.failBuild(ctx, b.ID, "Build permission revoked", log)
	}
	e = w.server.Store.finishClaimedBuildWithArtifact(ctx, b.ID, w.replica, "succeeded", "", artifact, log)
	if e == nil {
		_ = w.s3.RemoveObject(ctx, w.config.S3Bucket, workerUploadKey(b), minio.RemoveObjectOptions{})
	}
	return e
}
func (w *workersController) failOperation(ctx context.Context, id, message string) error {
	return w.server.Store.failOperation(ctx, id, message)
}
func (w *workersController) noteOperation(ctx context.Context, id, message string) error {
	return w.server.Store.noteOperationMessage(ctx, id, message)
}
func (w *workersController) management(ctx context.Context, pool WorkerRuntimePool, pod workerPod, path string, payload any) (map[string]any, int, error) {
	raw, _ := json.Marshal(payload)
	// Control planes outside the cluster reach runtimes through a fixed proxy base instead of Pod IPs.
	base := "http://" + net.JoinHostPort(pod.IP, "8083")
	if pool.RuntimeManagementBase != "" {
		base = pool.RuntimeManagementBase
	}
	req, e := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(raw))
	if e != nil {
		return nil, 0, e
	}
	req.Header.Set("Authorization", "Bearer "+pool.ManagementToken)
	req.Header.Set("Content-Type", "application/json")
	resp, e := w.http.Do(req)
	if e != nil {
		return nil, 0, e
	}
	defer resp.Body.Close()
	var result map[string]any
	e = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result)
	return result, resp.StatusCode, e
}
func workerMemberIDs(pods []workerPod) string {
	ids := []string{}
	for _, p := range pods {
		ids = append(ids, p.UID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}
func (w *workersController) processOperation(ctx context.Context, op WorkerOperation, state WorkerState, routesReady bool) error {
	if op.State != "committed" && now().Sub(op.Created) > 180*time.Second {
		return w.failOperation(ctx, op.ID, "Candidate preparation deadline exceeded")
	}
	release, ok := state.Releases[op.Release]
	if !ok {
		return w.failOperation(ctx, op.ID, "Release unavailable")
	}
	pool, ok := w.config.runtimePool(release.Pool)
	if !ok {
		return w.failOperation(ctx, op.ID, "Runtime pool unavailable")
	}
	artifact, ok := state.Artifacts[release.Artifact]
	if !ok {
		return w.failOperation(ctx, op.ID, "Artifact unavailable")
	}
	if op.State != "committed" {
		if _, e := w.artifactCode(ctx, artifact); e != nil {
			return w.failOperation(ctx, op.ID, "Artifact validation failed: "+e.Error())
		}
		// Claim the queued operation; losing the race means another replica owns
		// it and this cycle simply skips it.
		claimed, e := w.server.Store.claimOperation(ctx, op.ID, w.replica)
		if e != nil {
			return e
		}
		if !claimed {
			claimed, e = w.server.Store.claimStaleOperation(ctx, op.ID, w.replica)
			if e != nil {
				return e
			}
			if !claimed {
				return nil
			}
		}
		actor := state.OperationPrivate[op.ID].Actor
		auth, e := w.authorizeActor(ctx, actor, op.Service, op.Environment, workerOperationAction(op.Kind))
		if e != nil {
			return e
		}
		if auth != nil {
			return w.failOperation(ctx, op.ID, "Release permission revoked")
		}
	}
	pods, e := w.kube.runtimeMembers(ctx, pool.ID, pool.RuntimeService)
	if e != nil {
		return e
	}
	if len(pods) == 0 {
		return w.noteOperation(ctx, op.ID, "Waiting for serving runtime members")
	}
	if !routesReady {
		return w.noteOperation(ctx, op.ID, "Waiting for Gateway and HTTPRoute confirmation")
	}
	if op.State != "committed" {
		// Candidate validation loads the code once on a single serving member;
		// the other members lazy-load on first request. The edge config revision
		// and acks below report convergence.
		candidate := pods[0]
		// Pass the immutable descriptor and a short-lived artifact URL with the
		// validation request. A cold candidate therefore does not need another
		// control-plane round trip just to validate code.
		signed, e := w.downloadURL(ctx, artifact)
		if e != nil {
			return w.noteOperation(ctx, op.ID, "Artifact URL unavailable")
		}
		result, status, e := w.management(ctx, pool, candidate, "/validate", map[string]any{"release": release, "artifact": map[string]any{"download_url": signed, "size": artifact.Size, "archive_sha256": artifact.ArchiveDigest}})
		if e != nil {
			return w.noteOperation(ctx, op.ID, "Runtime validation temporarily unavailable")
		}
		if status != 200 {
			if result["code"] == "DEPENDENCY_UNAVAILABLE" {
				return w.noteOperation(ctx, op.ID, "Candidate dependency unavailable")
			}
			return w.failOperation(ctx, op.ID, "Candidate validation failed on "+candidate.Name)
		}
		// The pod-UID identity check assumes requests reach the exact pod
		// listed. Behind a proxied runtime_management_base the response may
		// come from any healthy member, so only release identity applies.
		uidMatches := result["pod_uid"] == candidate.UID
		if pool.RuntimeManagementBase != "" {
			uidMatches = true
		}
		if !uidMatches || result["release_id"] != release.ID || result["archive_sha256"] != release.ArchiveDigest {
			return w.failOperation(ctx, op.ID, fmt.Sprintf("Candidate identity mismatch (pod %s uid %s vs %s, release %s vs %s, archive %s vs %s)", candidate.Name, candidate.UID, result["pod_uid"], release.ID, result["release_id"], release.ArchiveDigest, result["archive_sha256"]))
		}
		after, e := w.kube.runtimeMembers(ctx, pool.ID, pool.RuntimeService)
		if e != nil {
			return e
		}
		if workerMemberIDs(after) != workerMemberIDs(pods) {
			return w.noteOperation(ctx, op.ID, "Runtime membership changed; validating again")
		}
		// Re-authorize immediately before commit: a permission revoked during
		// validation must stop the release.
		actor := state.OperationPrivate[op.ID].Actor
		auth, e := w.authorizeActor(ctx, actor, op.Service, op.Environment, workerOperationAction(op.Kind))
		if e != nil {
			return e
		}
		if auth != nil {
			return w.failOperation(ctx, op.ID, "Release permission revoked")
		}
		committed, e := w.server.Store.commitOperation(ctx, op)
		if e != nil {
			return e
		}
		if !committed {
			return nil
		}
	}
	// Publish once and freeze the first configuration revision that contains
	// this commit. Subsequent topology churn must not keep moving this
	// operation's acknowledgement target forward.
	revision := int64(op.EdgeRevision)
	if revision == 0 {
		revision, e = w.publishConfig(ctx)
		if e != nil {
			return w.noteOperation(ctx, op.ID, "Committed; waiting for configuration publication")
		}
		revision, e = w.server.Store.setOperationEdgeRevision(ctx, op.ID, revision)
		if e != nil {
			return e
		}
		if revision == 0 {
			return nil
		}
	}
	pods, e = w.kube.runtimeMembers(ctx, pool.ID, pool.RuntimeService)
	if e != nil {
		return e
	}
	if len(pods) == 0 {
		return w.noteOperation(ctx, op.ID, "Committed; waiting for serving runtime members")
	}
	acks, e := w.server.Store.ReadAcks(ctx)
	if e != nil {
		return e
	}
	after, e := w.kube.runtimeMembers(ctx, pool.ID, pool.RuntimeService)
	if e != nil {
		return e
	}
	if workerMemberIDs(after) != workerMemberIDs(pods) {
		return w.noteOperation(ctx, op.ID, "Committed; runtime membership changed")
	}
	// Convergence is observed through per-Pod acks. It is not a release gate:
	// the commit above is already durable and visible.
	for _, pod := range pods {
		ack, ok := acks[pod.UID]
		if !ok || ack.Pool != pool.ID || ack.Revision < int(revision) || now().Sub(ack.Time) > 10*time.Second {
			return nil
		}
	}
	return w.server.Store.confirmOperation(ctx, op)
}

// An infrastructure error after commit leaves a durable committed operation to retry; it does not silently undo the Release.

// processLocalBuild runs workers/builder/build-worker.mjs on the control-plane
// host. It is the dev-mode executor: no image build and no Kubernetes Job.
func (w *workersController) processLocalBuild(ctx context.Context, b WorkerBuild, private WorkerBuildPrivate) error {
	w.localMu.Lock()
	if state := w.localBuilds[b.ID]; state != nil && state.Running {
		w.localMu.Unlock()
		return nil
	}
	w.localBuilds[b.ID] = &workerLocalBuild{Running: true}
	w.localMu.Unlock()
	w.localWG.Add(1)
	go func() {
		defer w.localWG.Done()
		w.runLocalBuild(b, private)
	}()
	return nil
}

func (w *workersController) runLocalBuild(b WorkerBuild, private WorkerBuildPrivate) {
	defer func() {
		w.localMu.Lock()
		delete(w.localBuilds, b.ID)
		w.localMu.Unlock()
	}()
	parent := w.lifecycle
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 180*time.Second)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	leaseLost := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				owned, err := w.server.Store.renewBuildClaim(ctx, b.ID, w.replica)
				if err == nil && owned {
					continue
				}
				leaseLost <- struct{}{}
				cancel()
				return
			}
		}
	}()
	log, e := w.executeLocalBuilder(ctx, b, private)
	select {
	case <-leaseLost:
		// Another replica owns the row now. Its attempt is responsible for the
		// terminal transition; this process must not overwrite it.
		return
	default:
	}
	if e != nil {
		_ = w.failBuild(ctx, b.ID, "Builder failed", log)
		return
	}
	artifact, e := w.finalizeBuild(ctx, b)
	if e != nil {
		if errors.Is(e, errArtifactBeingCollected) {
			return
		}
		_ = w.failBuild(ctx, b.ID, "Artifact validation failed: "+e.Error(), log)
		return
	}
	// Re-check permission before the artifact becomes usable. This needs the IAM
	// partition, so it cannot run inside a Workers-only write.
	denied, e := w.authorizeActor(ctx, private.Actor, b.Service, b.Environment, "build.start")
	if e != nil {
		_ = w.failBuild(ctx, b.ID, "Build permission check unavailable", log)
		return
	}
	if denied != nil {
		_ = w.failBuild(ctx, b.ID, "Build permission revoked", log)
		return
	}
	e = w.server.Store.finishClaimedBuildWithArtifact(ctx, b.ID, w.replica, "succeeded", "", artifact, safeWorkerLog(private.Log+log))
	if e == nil {
		_ = w.s3.RemoveObject(ctx, w.config.S3Bucket, workerUploadKey(b), minio.RemoveObjectOptions{})
	}
}

func (w *workersController) executeLocalBuilder(ctx context.Context, b WorkerBuild, private WorkerBuildPrivate) (string, error) {
	if e := w.ensureBucket(ctx); e != nil {
		return "", e
	}
	upload, e := w.uploadURL(ctx, b)
	if e != nil {
		return "", e
	}
	source, e := w.sourceBundleURL(ctx, private.SourceKey)
	if e != nil {
		return "", e
	}
	dir, e := os.MkdirTemp("", "krun-local-build-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(dir)
	input, e := json.Marshal(map[string]any{"entrypoint": b.Entrypoint, "source_url": source, "assets": b.Assets, "upload_url": upload})
	if e != nil {
		return "", e
	}
	inputPath := filepath.Join(dir, "input.json")
	if e := os.WriteFile(inputPath, input, 0600); e != nil {
		return "", e
	}
	workDir := filepath.Join(dir, "work")
	if e := os.MkdirAll(workDir, 0700); e != nil {
		return "", e
	}
	script := w.config.BuilderScript
	if script == "" {
		script = "workers/builder/build-worker.mjs"
	}
	script, e = filepath.Abs(script)
	if e != nil {
		return "", e
	}
	cmd := exec.CommandContext(ctx, "node", filepath.Base(script))
	cmd.Dir = filepath.Dir(script)
	cmd.Env = append(os.Environ(), "KRUN_BUILD_INPUT="+inputPath, "KRUN_BUILD_WORK="+workDir)
	out, runErr := cmd.CombinedOutput()
	log := string(out)
	if len(log) > 32768 {
		log = log[:32768]
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return log, errors.New("local builder timed out")
		}
		return safeWorkerLog(log + "\n" + runErr.Error()), runErr
	}
	return log, nil
}
