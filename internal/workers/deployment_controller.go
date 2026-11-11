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
	if op.State == "committed" {
		scheduled, err := w.Service.Store.ScheduleCommittedOperation(ctx, op.ID)
		if err != nil || !scheduled {
			return err
		}
		return w.observeOperation(ctx, op, state, routesReady)
	}

	claim, owned, err := w.Service.Store.ClaimOperation(ctx, op.ID, w.Replica)
	if err != nil || !owned {
		return err
	}
	workCtx, guard := guardLease(ctx, func(ctx context.Context) (bool, error) {
		return w.Service.Store.RenewOperationClaim(ctx, claim)
	}, "operation lease lost")
	committed, err := w.validateOperation(workCtx, op, state, routesReady, claim)
	lost := guard.stop()
	if err != nil {
		return err
	}
	if !committed {
		return lost
	}
	op.EdgeRevision = 0
	return w.observeOperation(ctx, op, state, routesReady)
}

func (w *Controller) validateOperation(ctx context.Context, op domain.WorkerOperation, state domain.WorkerState, routesReady bool, claim storage.OperationClaim) (bool, error) {
	fail := func(message string) (bool, error) {
		return false, w.Service.Store.FailClaimedOperation(ctx, claim, message)
	}
	note := func(message string) (bool, error) {
		return false, w.Service.Store.NoteClaimedOperation(ctx, claim, message)
	}
	if time.Since(claim.StartedAt) > 180*time.Second {
		return fail("Candidate preparation deadline exceeded")
	}
	version, ok := state.Versions[op.Version]
	if !ok {
		return fail("Version unavailable")
	}
	placement, placed := domain.ActiveWorkerPlacement(&state, op.Worker)
	pool, ok := w.Config.RuntimePool(placement.Pool)
	if !placed || !ok {
		return fail("Runtime pool unavailable")
	}
	artifact, ok := state.Artifacts[version.Artifact]
	if !ok {
		return fail("Artifact unavailable")
	}
	if _, err := w.ArtifactCode(ctx, artifact); err != nil {
		return fail("Artifact validation failed: " + err.Error())
	}
	actor := state.OperationPrivate[op.ID].Actor
	action := appconfig.WorkerOperationAction(op.Kind)
	auth, e := w.AuthorizeActor(ctx, actor, op.Worker, op.Environment, action)
	if e != nil {
		return false, e
	}
	if auth != nil {
		return fail("Version permission revoked")
	}
	pods, e := w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
	if e != nil {
		return false, e
	}
	if len(pods) == 0 {
		return note("Waiting for serving runtime members")
	}
	if !routesReady {
		return note("Waiting for Gateway and HTTPRoute confirmation")
	}
	validationPool := pool
	members := pods
	base := ""
	role := "runtime"
	if w.Config.ValidatorPool != "" {
		var found bool
		validationPool, found = w.Config.RuntimePool(w.Config.ValidatorPool)
		if !found {
			return fail("Validation pool unavailable")
		}
		members, e = w.Kube.ValidatorMembers(ctx, validationPool.ID, validationPool.ValidatorService)
		if e != nil {
			return false, e
		}
		base = validationPool.ValidatorManagementBase
		role = "validator"
	}
	if len(members) == 0 {
		return note("Waiting for validation members")
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
		return note("Waiting for a runtime image supporting isolated validation")
	}
	if len(version.HyperdriveBindings) != 0 {
		if pool.PublicEgress != "direct" && pool.PrivateEgress != "direct" {
			return fail("Hyperdrive is unavailable in the serving pool")
		}
		if ready["hyperdrive_protocol"] != float64(2) {
			return note("Waiting for a runtime image supporting Hyperdrive")
		}
		for _, pod := range pods {
			if role == "runtime" && pod.UID == candidate.UID {
				continue
			}
			serving, status, err := w.Management(ctx, pool, pod, "", "/ready", nil)
			if err != nil || status != 200 ||
				serving["role"] != "runtime" || serving["pool"] != pool.ID ||
				serving["pod_uid"] != pod.UID || serving["hyperdrive_protocol"] != float64(2) {
				return note("Waiting for all serving members to support Hyperdrive")
			}
		}
		if w.Service.validateHyperdriveBindings(ctx, version.Organization, version.HyperdriveBindings) != nil {
			return fail("Hyperdrive configuration is unavailable")
		}
	}
	secrets, err := w.validationSecrets(ctx, version, pool.ID)
	if err != nil {
		return note("Candidate secrets unavailable")
	}

	grant, e := w.ArtifactReadGrant(ctx, artifact)
	if e != nil {
		return note("Artifact credentials unavailable")
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
		return note("Runtime validation temporarily unavailable")
	}
	if status != 200 {
		if result["code"] == "DEPENDENCY_UNAVAILABLE" {
			return note("Candidate dependency unavailable")
		}
		return fail("Candidate validation failed on member " + candidate.Name)
	}

	uidMatches := result["pod_uid"] == candidate.UID
	if base != "" {
		uidMatches = true
	}
	if !uidMatches || result["version_id"] != version.ID || result["archive_sha256"] != version.ArchiveDigest {
		return fail(fmt.Sprintf("Candidate identity mismatch (pod %s uid %s vs %s, version %s vs %s, archive %s vs %s)", candidate.Name, candidate.UID, result["pod_uid"], version.ID, result["version_id"], version.ArchiveDigest, result["archive_sha256"]))
	}
	var after []kubernetes.WorkerPod
	if w.Config.ValidatorPool == "" {
		after, e = w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
	} else {
		after, e = w.Kube.ValidatorMembers(ctx, validationPool.ID, validationPool.ValidatorService)
	}
	if e != nil {
		return false, e
	}
	if WorkerMemberIDs(after) != WorkerMemberIDs(members) {
		return note("Validation membership changed; validating again")
	}
	if role == "validator" && len(version.HyperdriveBindings) != 0 {
		serving, err := w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
		if err != nil {
			return false, err
		}
		if WorkerMemberIDs(serving) != WorkerMemberIDs(pods) {
			return note("Serving membership changed; validating again")
		}
	}

	auth, e = w.AuthorizeActor(ctx, actor, op.Worker, op.Environment, action)
	if e != nil {
		return false, e
	}
	if auth != nil {
		return fail("Version permission revoked")
	}
	return w.Service.Store.CommitOperation(ctx, claim)
}

func (w *Controller) observeOperation(ctx context.Context, op domain.WorkerOperation, state domain.WorkerState, routesReady bool) error {
	revision := int64(op.EdgeRevision)
	note := func(message string) error {
		return w.Service.Store.NoteCommittedOperation(ctx, op.ID, revision, message)
	}
	version, ok := state.Versions[op.Version]
	if !ok {
		return note("Committed; waiting for version")
	}
	if _, ok := state.Artifacts[version.Artifact]; !ok {
		return note("Committed; waiting for artifact")
	}
	placement, placed := domain.ActiveWorkerPlacement(&state, op.Worker)
	pool, ok := w.Config.RuntimePool(placement.Pool)
	if !placed || !ok {
		return note("Committed; waiting for runtime pool")
	}
	if !routesReady {
		return note("Committed; waiting for Gateway and HTTPRoute confirmation")
	}
	pinned, err := w.Service.Store.PinOperationEdgeRevision(ctx, op.ID, pool.ID)
	if err != nil {
		return err
	}
	if pinned == 0 {
		if _, err := w.PublishConfig(context.WithValue(ctx, storage.WorkerConfigPriorityKey{}, op.Worker)); err != nil {
			return note("Committed; waiting for configuration publication")
		}
		pinned, err = w.Service.Store.PinOperationEdgeRevision(ctx, op.ID, pool.ID)
		if err != nil {
			return err
		}
		if pinned == 0 {
			return note("Committed; waiting for configuration publication")
		}
	}
	revision = pinned
	pods, e := w.Kube.RuntimeMembers(ctx, pool.ID, pool.RuntimeService)
	if e != nil {
		return e
	}
	if len(pods) == 0 {
		return note("Committed; waiting for serving runtime members")
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
		return note("Committed; runtime membership changed")
	}

	for _, pod := range pods {
		ack, ok := acks[pod.UID]
		if !ok || ack.Pool != pool.ID || ack.Revision < int(revision) || time.Now().UTC().Sub(ack.Time) > 10*time.Second {
			return nil
		}
	}
	return w.Service.Store.ConfirmOperation(ctx, op.ID, pool.ID, revision)
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
