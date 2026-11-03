package workers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	kubernetes "github.com/krun-dev/krun/internal/adapters/kubernetes"
	runtimeclient "github.com/krun-dev/krun/internal/adapters/runtimeclient"
	appconfig "github.com/krun-dev/krun/internal/config"
	domain "github.com/krun-dev/krun/internal/domain"
	storage "github.com/krun-dev/krun/internal/storage"
)

func (w *Controller) FailOperation(ctx context.Context, id, message string) error {
	return w.Service.Store.FailOperation(ctx, id, message)
}

func (w *Controller) NoteOperation(ctx context.Context, id, message string) error {
	return w.Service.Store.NoteOperationMessage(ctx, id, message)
}

func (w *Controller) Management(ctx context.Context, pool appconfig.WorkerRuntimePool, pod kubernetes.WorkerPod, baseOverride, path string, payload any) (map[string]any, int, error) {
	return runtimeclient.Call(ctx, w.Http, pool, pod.IP, baseOverride, path, payload)
}

func WorkerMemberIDs(pods []kubernetes.WorkerPod) string {
	ids := []string{}
	for _, p := range pods {
		ids = append(ids, p.UID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func (w *Controller) ProcessOperation(ctx context.Context, op domain.WorkerOperation, state domain.WorkerState, routesReady bool) error {
	started, startErr := w.Service.Store.WorkerExecutionStarted(ctx, "operation", op.ID, op.Created)
	if startErr != nil {
		return startErr
	}
	if op.State != "committed" && time.Now().UTC().Sub(started) > 180*time.Second {
		return w.FailOperation(ctx, op.ID, "Candidate preparation deadline exceeded")
	}
	version, ok := state.Versions[op.Version]
	if !ok {
		return w.FailOperation(ctx, op.ID, "Version unavailable")
	}
	placement, placed := domain.ActiveWorkerPlacement(&state, version.Service)
	pool, ok := w.Config.RuntimePool(placement.Pool)
	if !placed || !ok {
		return w.FailOperation(ctx, op.ID, "Runtime pool unavailable")
	}
	artifact, ok := state.Artifacts[version.Artifact]
	if !ok {
		return w.FailOperation(ctx, op.ID, "Artifact unavailable")
	}
	if op.State != "committed" {

		claimed, e := w.Service.Store.ClaimOperation(ctx, op.ID, w.Replica)
		if e != nil {
			return e
		}
		if !claimed {
			claimed, e = w.Service.Store.ClaimStaleOperation(ctx, op.ID, w.Replica)
			if e != nil {
				return e
			}
			if !claimed {
				return nil
			}
		}
		if _, e := w.ArtifactCode(ctx, artifact); e != nil {
			return w.FailOperation(ctx, op.ID, "Artifact validation failed: "+e.Error())
		}
		actor := state.OperationPrivate[op.ID].Actor
		auth, e := w.AuthorizeActor(ctx, actor, op.Service, op.Environment, appconfig.WorkerOperationAction(op.Kind))
		if e != nil {
			return e
		}
		if auth != nil {
			return w.FailOperation(ctx, op.ID, "Version permission revoked")
		}
	}
	pods, e := w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
	if e != nil {
		return e
	}
	if len(pods) == 0 {
		return w.NoteOperation(ctx, op.ID, "Waiting for serving runtime members")
	}
	if !routesReady {
		return w.NoteOperation(ctx, op.ID, "Waiting for Gateway and HTTPRoute confirmation")
	}
	if op.State != "committed" {
		validators, validationErr := w.Kube.ValidatorMembers(ctx, pool.ID, pool.ValidatorService)
		if validationErr != nil {
			return validationErr
		}
		if len(validators) == 0 {
			return w.NoteOperation(ctx, op.ID, "Waiting for isolated validator members")
		}

		candidate := validators[0]

		signed, e := w.DownloadURL(ctx, artifact)
		if e != nil {
			return w.NoteOperation(ctx, op.ID, "Artifact URL unavailable")
		}
		result, status, e := w.Management(ctx, pool, candidate, pool.ValidatorManagementBase, "/validate", map[string]any{"version": version, "artifact": map[string]any{"download_url": signed, "size": artifact.Size, "archive_sha256": artifact.ArchiveDigest}})
		if e != nil {
			return w.NoteOperation(ctx, op.ID, "Runtime validation temporarily unavailable")
		}
		if status != 200 {
			if result["code"] == "DEPENDENCY_UNAVAILABLE" {
				return w.NoteOperation(ctx, op.ID, "Candidate dependency unavailable")
			}
			return w.FailOperation(ctx, op.ID, "Candidate validation failed on isolated validator "+candidate.Name)
		}

		uidMatches := result["pod_uid"] == candidate.UID
		if pool.ValidatorManagementBase != "" {
			uidMatches = true
		}
		if !uidMatches || result["version_id"] != version.ID || result["archive_sha256"] != version.ArchiveDigest {
			return w.FailOperation(ctx, op.ID, fmt.Sprintf("Candidate identity mismatch (pod %s uid %s vs %s, version %s vs %s, archive %s vs %s)", candidate.Name, candidate.UID, result["pod_uid"], version.ID, result["version_id"], version.ArchiveDigest, result["archive_sha256"]))
		}
		after, e := w.Kube.ValidatorMembers(ctx, pool.ID, pool.ValidatorService)
		if e != nil {
			return e
		}
		if WorkerMemberIDs(after) != WorkerMemberIDs(validators) {
			return w.NoteOperation(ctx, op.ID, "Validator membership changed; validating again")
		}

		actor := state.OperationPrivate[op.ID].Actor
		auth, e := w.AuthorizeActor(ctx, actor, op.Service, op.Environment, appconfig.WorkerOperationAction(op.Kind))
		if e != nil {
			return e
		}
		if auth != nil {
			return w.FailOperation(ctx, op.ID, "Version permission revoked")
		}
		committed, e := w.Service.Store.CommitOperation(ctx, op)
		if e != nil {
			return e
		}
		if !committed {
			return nil
		}
	}

	revision := int64(op.EdgeRevision)
	if revision == 0 {
		revision, e = w.PublishConfig(context.WithValue(ctx, storage.WorkerConfigPriorityKey{}, op.Service))
		if e != nil {
			return w.NoteOperation(ctx, op.ID, "Committed; waiting for configuration publication")
		}
		revision, e = w.Service.Store.SetOperationEdgeRevision(ctx, op.ID, revision)
		if e != nil {
			return e
		}
		if revision == 0 {
			return nil
		}
	}
	pods, e = w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
	if e != nil {
		return e
	}
	if len(pods) == 0 {
		return w.NoteOperation(ctx, op.ID, "Committed; waiting for serving runtime members")
	}
	acks, e := w.Service.Store.ReadAcks(ctx)
	if e != nil {
		return e
	}
	after, e := w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
	if e != nil {
		return e
	}
	if WorkerMemberIDs(after) != WorkerMemberIDs(pods) {
		return w.NoteOperation(ctx, op.ID, "Committed; runtime membership changed")
	}

	for _, pod := range pods {
		ack, ok := acks[pod.UID]
		if !ok || ack.Pool != pool.ID || ack.Revision < int(revision) || time.Now().UTC().Sub(ack.Time) > 10*time.Second {
			return nil
		}
	}
	return w.Service.Store.ConfirmOperation(ctx, op)
}
