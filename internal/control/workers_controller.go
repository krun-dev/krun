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
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

type workersController struct {
	server *Server
	config *WorkersConfig
	s3     *minio.Client
	// s3Builder signs builder upload URLs against the in-cluster endpoint; signing is offline, no connectivity needed.
	s3Builder *minio.Client
	kube   *workerKubernetes
	http   *http.Client
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
	return &workersController{server: s, config: s.Config.Workers, s3: s3, s3Builder: s3Builder, kube: k, http: &http.Client{Timeout: 8 * time.Second}}, nil
}
func (w *workersController) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A session advisory lock elects one reconciler across control replicas, without holding the IAM row lock during network operations.
			if w.server.Store.pool == nil {
				continue
			}
			conn, e := w.server.Store.pool.Acquire(ctx)
			if e != nil {
				continue
			}
			var locked bool
			e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(724618031)").Scan(&locked)
			if e == nil && locked {
				cycle, cancel := context.WithTimeout(ctx, 25*time.Second)
				e = w.reconcile(cycle)
				cancel()
				unlock, c := context.WithTimeout(context.Background(), time.Second)
				_, _ = conn.Exec(unlock, "SELECT pg_advisory_unlock(724618031)")
				c()
			}
			conn.Release()
			if e != nil && ctx.Err() == nil {
				fmt.Println("workers reconcile:", e)
			}
		}
	}
}
func (w *workersController) read(ctx context.Context) (*WorkerState, error) {
	var out *WorkerState
	e := w.server.Store.Update(ctx, func(st *State) error {
		if st.Workers == nil {
			st.Workers = newWorkerState()
		}
		raw, e := json.Marshal(st.Workers)
		if e != nil {
			return e
		}
		out = newWorkerState()
		return json.Unmarshal(raw, out)
	})
	return out, e
}
func (w *workersController) reconcile(ctx context.Context) error {
	state, e := w.read(ctx)
	if e != nil {
		return e
	}
	builds := []WorkerBuild{}
	for _, b := range state.Builds {
		if !workerTerminal(b.State) {
			builds = append(builds, b)
		}
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].Created.Before(builds[j].Created) })
	var failures []error
	for _, b := range builds {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = w.processBuild(ctx, b, state.BuildPrivate[b.ID]); e != nil {
			failures = append(failures, e)
		}
	}
	// Each service has at most three hosts; Gateway API limits hosts per HTTPRoute.
	routesReady := map[string]bool{}
	for _, service := range state.Services {
		hosts := []string{}
		for _, env := range service.Environments {
			if env.Enabled {
				hosts = append(hosts, env.Host)
			}
		}
		ready, routeError := w.kube.ensureRoutes(ctx, w.config, "workers-"+digest(service.Organization + "/" + service.Service)[:24], hosts)
		routesReady[service.Service] = ready && routeError == nil
		if routeError != nil {
			failures = append(failures, routeError)
		}
	}
	ops := []WorkerOperation{}
	for _, op := range state.Operations {
		if !workerTerminal(op.State) {
			ops = append(ops, op)
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Created.Before(ops[j].Created) })
	for _, op := range ops {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = w.processOperation(ctx, op, state, routesReady[op.Service]); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (w *workersController) failBuild(ctx context.Context, id, message, log string) error {
	return w.server.Store.Update(ctx, func(st *State) error {
		b := st.Workers.Builds[id]
		if workerTerminal(b.State) {
			return nil
		}
		b.State = "failed"
		b.Error = message
		b.Finished = workerCloneTime(now())
		st.Workers.Builds[id] = b
		private := st.Workers.BuildPrivate[id]
		private.Log = safeWorkerLog(log)
		st.Workers.BuildPrivate[id] = private
		return nil
	})
}
func (w *workersController) processBuild(ctx context.Context, b WorkerBuild, private WorkerBuildPrivate) error {
	var allowed error
	if e := w.server.Store.Update(ctx, func(st *State) error {
		allowed = w.server.workerAuthorizeActor(st, private.Actor, b.Service, b.Environment, "build.start")
		return nil
	}); e != nil {
		return e
	}
	if allowed != nil {
		return w.failBuild(ctx, b.ID, "Build permission revoked", "")
	}
	if now().Sub(b.Created) > 180*time.Second {
		return w.failBuild(ctx, b.ID, "Build deadline exceeded", w.kube.buildLog(ctx, b.Job))
	}
	if b.Repository != nil && len(private.Files) == 0 {
		var err error
		b, private, err = w.prepareRepositoryBuild(ctx, b, private)
		if err != nil {
			return w.failBuild(ctx, b.ID, err.Error(), private.Log)
		}
	}
	var job workerJob
	e := w.kube.call(ctx, "GET", w.kube.jobs()+"/"+b.Job, nil, &job)
	if kubeMissing(e) {
		if e = w.ensureBucket(ctx); e != nil {
			return e
		}
		signed, e := w.uploadURL(ctx, b)
		if e != nil {
			return e
		}
		if e = w.kube.ensureBuildJob(ctx, b, private, signed, w.config.BuilderImage, w.config.BuilderPullPolicy); e != nil {
			return e
		}
		return w.server.Store.Update(ctx, func(st *State) error {
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
	e = w.server.Store.Update(ctx, func(st *State) error {
		current := st.Workers.Builds[b.ID]
		if workerTerminal(current.State) {
			return nil
		}
		if auth := w.server.workerAuthorizeActor(st, private.Actor, b.Service, b.Environment, "build.start"); auth != nil {
			current.State = "failed"
			current.Error = "Build permission revoked"
		} else {
			if old, ok := st.Workers.Artifacts[artifact.ID]; ok {
				artifact = old
			}
			st.Workers.Artifacts[artifact.ID] = artifact
			current.State = "succeeded"
			current.Artifact = artifact.ID
		}
		current.Finished = workerCloneTime(now())
		st.Workers.Builds[b.ID] = current
		p := st.Workers.BuildPrivate[b.ID]
		p.Log = log
		st.Workers.BuildPrivate[b.ID] = p
		return nil
	})
	if e == nil {
		_ = w.s3.RemoveObject(ctx, w.config.S3Bucket, workerUploadKey(b), minio.RemoveObjectOptions{})
	}
	return e
}
func (w *workersController) failOperation(ctx context.Context, id, message string) error {
	return w.server.Store.Update(ctx, func(st *State) error { workerFailOperation(st, id, message); return nil })
}
func (w *workersController) noteOperation(ctx context.Context, id, message string) error {
	return w.server.Store.Update(ctx, func(st *State) error {
		op := st.Workers.Operations[id]
		if !workerTerminal(op.State) {
			op.Error = message
			st.Workers.Operations[id] = op
		}
		return nil
	})
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
func (w *workersController) processOperation(ctx context.Context, op WorkerOperation, state *WorkerState, routesReady bool) error {
	if op.State != "committed" && now().Sub(op.Created) > 180*time.Second {
		return w.failOperation(ctx, op.ID, "Candidate preparation deadline exceeded")
	}
	deployment, ok := state.Deployments[op.Deployment]
	if !ok {
		return w.failOperation(ctx, op.ID, "Deployment unavailable")
	}
	artifact, ok := state.Artifacts[deployment.Artifact]
	if !ok {
		return w.failOperation(ctx, op.ID, "Artifact unavailable")
	}
	if op.State != "committed" {
		if _, e := w.artifactCode(ctx, artifact); e != nil {
			return w.failOperation(ctx, op.ID, "Artifact validation failed: "+e.Error())
		}
		canProceed := true
		if e := w.server.Store.Update(ctx, func(st *State) error {
			current := st.Workers.Operations[op.ID]
			if workerTerminal(current.State) {
				canProceed = false
				return nil
			}
			if e := w.server.workerAuthorizeActor(st, st.Workers.OperationPrivate[op.ID].Actor, op.Service, op.Environment, "service."+op.Kind); e != nil {
				workerFailOperation(st, op.ID, "Release permission revoked")
				canProceed = false
				return nil
			}
			current.State = "validating"
			current.Steps[0].State = "done"
			current.Steps[1].State = "running"
			current.Error = ""
			st.Workers.Operations[op.ID] = current
			return nil
		}); e != nil {
			return e
		}
		if !canProceed {
			return nil
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
		for _, pod := range pods {
			result, status, e := w.management(ctx, pod, "/validate", map[string]any{"deployment_id": deployment.ID})
			if e != nil {
				return w.noteOperation(ctx, op.ID, "Runtime validation temporarily unavailable")
			}
			if status != 200 {
				if result["code"] == "DEPENDENCY_UNAVAILABLE" {
					return w.noteOperation(ctx, op.ID, "Candidate dependency unavailable")
				}
				return w.failOperation(ctx, op.ID, "Candidate validation failed on "+pod.Name)
			}
			// The pod-UID identity check assumes requests reach the exact pod
			// listed. Behind a proxied runtime_management_base the response may
			// come from any healthy member, so only artifact identity applies.
			uidMatches := result["pod_uid"] == pod.UID
			if w.config.RuntimeManagementBase != "" {
				uidMatches = true
			}
			if !uidMatches || result["deployment_id"] != deployment.ID || result["archive_sha256"] != deployment.ArchiveDigest {
				return w.failOperation(ctx, op.ID, fmt.Sprintf("Candidate identity mismatch (pod %s uid %s vs %s, deployment %s vs %s, archive %s vs %s)", pod.Name, pod.UID, result["pod_uid"], deployment.ID, result["deployment_id"], deployment.ArchiveDigest, result["archive_sha256"]))
			}
		}
		after, e := w.kube.runtimeMembers(ctx, w.config.RuntimeService)
		if e != nil {
			return e
		}
		if workerMemberIDs(after) != workerMemberIDs(pods) {
			return w.noteOperation(ctx, op.ID, "Runtime membership changed; validating again")
		}
		return w.server.Store.Update(ctx, func(st *State) error {
			current := st.Workers.Operations[op.ID]
			if workerTerminal(current.State) || current.State == "committed" {
				return nil
			}
			ws, ok := st.Workers.Services[op.Service]
			if !ok {
				workerFailOperation(st, op.ID, "Service unavailable")
				return nil
			}
			env := ws.Environments[op.Environment]
			if e := w.server.workerAuthorizeActor(st, st.Workers.OperationPrivate[op.ID].Actor, op.Service, op.Environment, "service."+op.Kind); e != nil {
				workerFailOperation(st, op.ID, "Release permission revoked")
				return nil
			}
			if !env.Enabled || env.Revision != op.ExpectedRevision || env.ConfigRevision != op.ConfigRevision {
				workerFailOperation(st, op.ID, "Release or configuration revision conflict")
				return nil
			}
			// A validated rollback may replace a release whose adoption is stalled.
			for id, previous := range st.Workers.Operations {
				if id != op.ID && previous.Service == op.Service && previous.Environment == op.Environment && previous.State == "committed" {
					previous.State = "superseded"
					previous.Error = "Replaced by " + op.ID
					previous.Finished = workerCloneTime(now())
					st.Workers.Operations[id] = previous
				}
			}
			env.Revision++
			env.Deployment = op.Deployment
			ws.Environments[op.Environment] = env
			st.Workers.Services[op.Service] = ws
			st.Workers.Revision++
			current.State = "committed"
			current.Error = ""
			current.ReleaseRevision = env.Revision
			current.SnapshotRevision = st.Workers.Revision
			current.Steps[1].State = "done"
			current.Steps[2].State = "done"
			current.Steps[3].State = "running"
			st.Workers.Operations[op.ID] = current
			return nil
		})
	}
	for _, pod := range pods {
		result, status, e := w.management(ctx, pod, "/adopt", map[string]any{"deployment_id": deployment.ID, "minimum_revision": op.SnapshotRevision})
		if e != nil || status != 200 || result["pod_uid"] != pod.UID || result["deployment_id"] != deployment.ID || result["archive_sha256"] != deployment.ArchiveDigest {
			return w.noteOperation(ctx, op.ID, "Committed; waiting for runtime adoption")
		}
		rev, ok := result["revision"].(float64)
		if !ok || int(rev) < op.SnapshotRevision {
			return w.noteOperation(ctx, op.ID, "Committed; waiting for snapshot revision")
		}
	}
	after, e := w.kube.runtimeMembers(ctx, w.config.RuntimeService)
	if e != nil {
		return e
	}
	if workerMemberIDs(after) != workerMemberIDs(pods) {
		return w.noteOperation(ctx, op.ID, "Committed; runtime membership changed")
	}
	return w.server.Store.Update(ctx, func(st *State) error {
		current := st.Workers.Operations[op.ID]
		if current.State != "committed" {
			return nil
		}
		ws := st.Workers.Services[op.Service]
		env := ws.Environments[op.Environment]
		if env.Deployment != op.Deployment || env.Revision != op.ReleaseRevision {
			current.State = "superseded"
			current.Error = "Release superseded"
		} else {
			for _, pod := range pods {
				ack, ok := st.Workers.Acks[pod.UID]
				if !ok || ack.Pool != "default" || ack.Revision < op.SnapshotRevision || now().Sub(ack.Time) > 10*time.Second {
					return nil
				}
			}
			env.Confirmed = op.Deployment
			ws.Environments[op.Environment] = env
			st.Workers.Services[op.Service] = ws
			current.State = "available"
			current.Error = ""
			current.Steps[3].State = "done"
			key := op.Service + "/" + op.Environment
			st.Workers.Histories[key] = append([]WorkerHistory{{op.ReleaseRevision, op.Deployment, op.ID, op.Kind, now()}}, st.Workers.Histories[key]...)
		}
		current.Finished = workerCloneTime(now())
		st.Workers.Operations[op.ID] = current
		return nil
	})
}

// An infrastructure error after commit leaves a durable committed operation to retry; it does not silently undo the Release.
