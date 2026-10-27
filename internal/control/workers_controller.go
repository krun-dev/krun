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
	// s3Builder signs builder upload URLs against the in-cluster endpoint; signing is offline, no connectivity needed.
	s3Builder *minio.Client
	kube      *workerKubernetes
	http      *http.Client
	// Parsed asset manifests are immutable per artifact; cache them so presigning
	// a single file does not re-download the archive.
	assetMu    sync.RWMutex
	assetCache map[string]workerCachedAssets
	// Edge config publication is single-writer: only the elected controller
	// assigns revisions and writes snapshots.
	configMu       sync.Mutex
	configLoaded   bool
	configDirty    bool
	configBucket   bool
	configLast     *workerConfigProjection
	configRevision int64
	configFloor    int64
	// Local build executor state for builder_mode: local.
	localMu     sync.Mutex
	localBuilds map[string]*workerLocalBuild
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
	k, e := newWorkerKubernetes(s.Config.Workers.Namespace)
	if e != nil {
		return nil, e
	}
	return &workersController{server: s, config: s.Config.Workers, s3: s3, putObject: s3.PutObject, s3Builder: s3Builder, kube: k, http: &http.Client{Timeout: 8 * time.Second}, assetCache: map[string]workerCachedAssets{}, localBuilds: map[string]*workerLocalBuild{}, replica: workerReplicaID()}, nil
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
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
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

// read loads the Workers partition. The loaded state is already a fresh copy
// per call, so the previous marshal/unmarshal round trip only to deep-copy was
// redundant.
func (w *workersController) read(ctx context.Context) (*WorkerState, error) {
	st, e := w.server.Store.Open(ctx, PartitionWorkers)
	if e != nil {
		return nil, e
	}
	if st.Workers == nil {
		return newWorkerState(), nil
	}
	return st.Workers, nil
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
	topology, e := w.server.Store.Open(ctx, PartitionWorkers)
	if e != nil {
		return e
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
		if e = w.processBuild(ctx, b, pending.BuildPrivate[b.ID]); e != nil {
			failures = append(failures, e)
		}
	}
	// One shared static route per domain; it does not scale with services.
	// Each route lives on the Gateway instance that domain is bound to. Assets
	// are written to both the public and internal object prefixes, so the first
	// domain serves pub/ and later ones serve int/; that keeps the existing
	// bucket layout working while every domain gets its own origin.
	desiredStatic := map[string]bool{}
	for i, d := range w.config.Domains {
		desiredStatic["workers-static-"+d.ID] = true
		prefix := "int"
		if i == 0 {
			prefix = "pub"
		}
		if e := w.kube.ensureStaticRoute(ctx, w.config, d.ID, prefix); e != nil {
			failures = append(failures, e)
		}
	}
	// Static routes from a previous catalog (renamed or removed domains) would
	// keep serving objects on hosts nobody configured any more.
	for _, n := range staleWorkerRoutes(ctx, w.kube, "workers-static-", desiredStatic) {
		if e := w.kube.deleteRoute(ctx, n); e != nil {
			failures = append(failures, e)
		}
	}
	// One HTTPRoute per (service, Gateway instance). Hosts are grouped by the
	// Gateway their domain is bound to, so routes never carry a hostname the
	// instance is not meant to serve.
	routesReady := map[string]bool{}
	for _, service := range topology.Workers.Services {
		hostsByGateway := map[string][]string{}
		for _, env := range service.Environments {
			if !env.Enabled {
				continue
			}
			// Hosts are derived from the domain catalog, never persisted, so a
			// catalog change cannot leave stale hosts behind.
			for _, id := range service.Domains {
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
		ready, routeError := w.kube.ensureRoutes(ctx, w.config, "workers-"+digest(service.Organization+"/"+service.Service)[:24], hostsByGateway)
		routesReady[service.Service] = ready && routeError == nil
		if routeError != nil {
			failures = append(failures, routeError)
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
func (w *workersController) failBuild(ctx context.Context, id, message, log string) error {
	return w.server.Store.finishBuild(ctx, id, "failed", message, "", safeWorkerLog(log))
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
		return w.server.Store.Update(ctx, PartitionWorkers, func(st *State) error {
			current := st.Workers.Builds[b.ID]
			if current.State == "queued" || current.State == "fetching" {
				current.State = "building"
				st.Workers.Builds[b.ID] = current
			}
			return nil
		})
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
	e = w.server.Store.finishBuildWithArtifact(ctx, b.ID, "succeeded", "", artifact, log)
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
func (w *workersController) management(ctx context.Context, pod workerPod, path string, payload any) (map[string]any, int, error) {
	raw, _ := json.Marshal(payload)
	// Control planes outside the cluster reach runtimes through a fixed proxy base instead of Pod IPs.
	base := "http://" + net.JoinHostPort(pod.IP, "8083")
	if w.config.RuntimeManagementBase != "" {
		base = w.config.RuntimeManagementBase
	}
	req, e := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(raw))
	if e != nil {
		return nil, 0, e
	}
	req.Header.Set("Authorization", "Bearer "+w.config.ManagementToken)
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
		auth, e := w.authorizeActor(ctx, actor, op.Service, op.Environment, "service."+op.Kind)
		if e != nil {
			return e
		}
		if auth != nil {
			return w.failOperation(ctx, op.ID, "Release permission revoked")
		}
	}
	pods, e := w.kube.runtimeMembers(ctx, w.config.RuntimeService)
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
		result, status, e := w.management(ctx, candidate, "/validate", map[string]any{"release": release, "artifact": map[string]any{"download_url": signed, "size": artifact.Size, "archive_sha256": artifact.ArchiveDigest}})
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
		if w.config.RuntimeManagementBase != "" {
			uidMatches = true
		}
		if !uidMatches || result["release_id"] != release.ID || result["archive_sha256"] != release.ArchiveDigest {
			return w.failOperation(ctx, op.ID, fmt.Sprintf("Candidate identity mismatch (pod %s uid %s vs %s, release %s vs %s, archive %s vs %s)", candidate.Name, candidate.UID, result["pod_uid"], release.ID, result["release_id"], release.ArchiveDigest, result["archive_sha256"]))
		}
		after, e := w.kube.runtimeMembers(ctx, w.config.RuntimeService)
		if e != nil {
			return e
		}
		if workerMemberIDs(after) != workerMemberIDs(pods) {
			return w.noteOperation(ctx, op.ID, "Runtime membership changed; validating again")
		}
		// Re-authorize immediately before commit: a permission revoked during
		// validation must stop the release.
		actor := state.OperationPrivate[op.ID].Actor
		auth, e := w.authorizeActor(ctx, actor, op.Service, op.Environment, "service."+op.Kind)
		if e != nil {
			return e
		}
		if auth != nil {
			return w.failOperation(ctx, op.ID, "Release permission revoked")
		}
		if _, e := w.server.Store.commitOperation(ctx, op); e != nil {
			return e
		}
	}
	// Publish the configuration revision that contains this commit. Runtimes
	// learn about it through the incremental feed; the controller does not run a
	// per-Pod management RPC, so a slow member cannot block the release pipeline.
	revision, e := w.publishConfig(ctx)
	if e != nil {
		return w.noteOperation(ctx, op.ID, "Committed; waiting for configuration publication")
	}
	if e := w.server.Store.setOperationEdgeRevision(ctx, op.ID, revision); e != nil {
		return e
	}
	pods, e = w.kube.runtimeMembers(ctx, w.config.RuntimeService)
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
	after, e := w.kube.runtimeMembers(ctx, w.config.RuntimeService)
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
		if !ok || ack.Pool != "default" || ack.Revision < int(revision) || now().Sub(ack.Time) > 10*time.Second {
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
	if e := w.server.Store.Update(ctx, PartitionWorkers, func(st *State) error {
		current := st.Workers.Builds[b.ID]
		if current.State == "queued" || current.State == "fetching" {
			current.State = "building"
			st.Workers.Builds[b.ID] = current
		}
		return nil
	}); e != nil {
		w.localMu.Lock()
		delete(w.localBuilds, b.ID)
		w.localMu.Unlock()
		return e
	}
	go w.runLocalBuild(b, private)
	return nil
}

func (w *workersController) runLocalBuild(b WorkerBuild, private WorkerBuildPrivate) {
	defer func() {
		w.localMu.Lock()
		delete(w.localBuilds, b.ID)
		w.localMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	log, e := w.executeLocalBuilder(ctx, b, private)
	if e != nil {
		_ = w.failBuild(ctx, b.ID, "Builder failed", log)
		return
	}
	artifact, e := w.finalizeBuild(ctx, b)
	if e != nil {
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
	e = w.server.Store.finishBuildWithArtifact(ctx, b.ID, "succeeded", "", artifact, safeWorkerLog(private.Log+log))
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
