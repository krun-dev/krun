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
	placement, placed := domain.ActiveWorkerPlacement(&state, version.Worker)
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
		auth, e := w.AuthorizeActor(ctx, actor, op.Worker, op.Environment, appconfig.WorkerOperationAction(op.Kind))
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
		validationPool := pool
		members := pods
		base := ""
		role := "runtime"
		if w.Config.ValidatorPool != "" {
			var found bool
			validationPool, found = w.Config.RuntimePool(w.Config.ValidatorPool)
			if !found {
				return w.FailOperation(ctx, op.ID, "Validation pool unavailable")
			}
			members, e = w.Kube.ValidatorMembers(ctx, validationPool.ID, validationPool.ValidatorService)
			if e != nil {
				return e
			}
			base = validationPool.ValidatorManagementBase
			role = "validator"
		}
		if len(members) == 0 {
			return w.NoteOperation(ctx, op.ID, "Waiting for validation members")
		}
		candidate := members[0]

		// Older serving images also expose /validate, but do not isolate its
		// loader and bindings. Check capability before executing any candidate.
		ready, status, err := w.Management(ctx, validationPool, candidate, base, "/ready", nil)
		readyMatches := ready["validation_protocol"] == float64(1) &&
			ready["role"] == role &&
			ready["pool"] == validationPool.ID
		readyUIDMatches := base != "" || ready["pod_uid"] == candidate.UID
		if err != nil || status != 200 || !readyMatches || !readyUIDMatches {
			return w.NoteOperation(ctx, op.ID, "Waiting for a runtime image supporting isolated validation")
		}
		if len(version.HyperdriveBindings) != 0 {
			if w.ValidateHyperdrive == nil || (pool.PublicEgress != "direct" && pool.PrivateEgress != "direct") {
				return w.FailOperation(ctx, op.ID, "Hyperdrive is unavailable in the serving pool")
			}
			if ready["hyperdrive_protocol"] != float64(1) {
				return w.NoteOperation(ctx, op.ID, "Waiting for a runtime image supporting Hyperdrive")
			}
			for _, pod := range pods {
				if role == "runtime" && pod.UID == candidate.UID {
					continue
				}
				serving, status, err := w.Management(ctx, pool, pod, "", "/ready", nil)
				if err != nil || status != 200 ||
					serving["role"] != "runtime" || serving["pool"] != pool.ID ||
					serving["pod_uid"] != pod.UID || serving["hyperdrive_protocol"] != float64(1) {
					return w.NoteOperation(ctx, op.ID, "Waiting for all serving members to support Hyperdrive")
				}
			}
			if w.ValidateHyperdrive(ctx, version, pool) != nil {
				return w.FailOperation(ctx, op.ID, "Hyperdrive database is unavailable in the serving pool")
			}
		}
		secrets, err := w.validationSecrets(ctx, version, pool.ID)
		if err != nil {
			return w.NoteOperation(ctx, op.ID, "Candidate secrets unavailable")
		}

		grant, e := w.ArtifactReadGrant(ctx, artifact)
		if e != nil {
			return w.NoteOperation(ctx, op.ID, "Artifact credentials unavailable")
		}
		payload := map[string]any{
			"version": version,
			"secrets": secrets,
			"artifact": map[string]any{
				"storage":        runtimeclient.ReadGrantPayload(grant),
				"object_key":     "bundle.tar.gz",
				"size":           artifact.Size,
				"archive_sha256": artifact.ArchiveDigest,
			},
		}
		result, status, e := w.Management(ctx, validationPool, candidate, base, "/validate", payload)
		if e != nil {
			return w.NoteOperation(ctx, op.ID, "Runtime validation temporarily unavailable")
		}
		if status != 200 {
			if result["code"] == "DEPENDENCY_UNAVAILABLE" {
				return w.NoteOperation(ctx, op.ID, "Candidate dependency unavailable")
			}
			return w.FailOperation(ctx, op.ID, "Candidate validation failed on member "+candidate.Name)
		}

		uidMatches := result["pod_uid"] == candidate.UID
		if base != "" {
			uidMatches = true
		}
		if !uidMatches || result["version_id"] != version.ID || result["archive_sha256"] != version.ArchiveDigest {
			return w.FailOperation(ctx, op.ID, fmt.Sprintf("Candidate identity mismatch (pod %s uid %s vs %s, version %s vs %s, archive %s vs %s)", candidate.Name, candidate.UID, result["pod_uid"], version.ID, result["version_id"], version.ArchiveDigest, result["archive_sha256"]))
		}
		var after []kubernetes.WorkerPod
		if w.Config.ValidatorPool == "" {
			after, e = w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
		} else {
			after, e = w.Kube.ValidatorMembers(ctx, validationPool.ID, validationPool.ValidatorService)
		}
		if e != nil {
			return e
		}
		if WorkerMemberIDs(after) != WorkerMemberIDs(members) {
			return w.NoteOperation(ctx, op.ID, "Validation membership changed; validating again")
		}
		if role == "validator" && len(version.HyperdriveBindings) != 0 {
			serving, err := w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
			if err != nil {
				return err
			}
			if WorkerMemberIDs(serving) != WorkerMemberIDs(pods) {
				return w.NoteOperation(ctx, op.ID, "Serving membership changed; validating again")
			}
		}

		actor := state.OperationPrivate[op.ID].Actor
		auth, e := w.AuthorizeActor(ctx, actor, op.Worker, op.Environment, appconfig.WorkerOperationAction(op.Kind))
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
		revision, e = w.PublishConfig(context.WithValue(ctx, storage.WorkerConfigPriorityKey{}, op.Worker))
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

// Only this candidate's secrets travel to the selected member; a shared
// validator never receives another pool's serving token or general read access.
func (w *Controller) validationSecrets(ctx context.Context, version domain.WorkerVersion, pool string) (map[string]any, error) {
	values := map[string]string{}
	if version.SecretDigest != "" {
		digest, encrypted, err := w.Service.Store.EncryptedVersionSecrets(ctx, version.ID, pool)
		if err != nil {
			return nil, err
		}
		if digest != version.SecretDigest {
			return nil, fmt.Errorf("candidate secret binding mismatch")
		}
		values, err = DecryptWorkerSecrets(w.Config, digest, encrypted)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"version_id":    version.ID,
		"secret_digest": version.SecretDigest,
		"values":        values,
	}, nil
}
