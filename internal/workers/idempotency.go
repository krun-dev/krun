package workers

import (
	"encoding/json"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	authorization "github.com/krun-dev/krun/internal/authorization"
	domain "github.com/krun-dev/krun/internal/domain"
)

func WorkerIdempotent(st *domain.State, key string, in any) (string, error) {
	if key == "" {
		return "", domain.Invalid("IDEMPOTENCY_REQUIRED", "An idempotency_key is required")
	}
	raw, _ := json.Marshal(in)
	record, ok := st.Workers.Idempotency[key]
	if !ok {
		return "", nil
	}
	if record.Digest != workerartifact.BytesDigest(raw) {
		return "", domain.Conflict("IDEMPOTENCY_CONFLICT", "Idempotency key used for another request")
	}
	return record.ID, nil
}

func WorkerIdempotencyKey(t authorization.Target, p authorization.Principal, kind, env, key string) string {
	return t.Organization + "/" + t.ID + "/" + p.User.ID + "/" + kind + "/" + env + "/" + key
}

func WorkerRemember(st *domain.State, key string, in any, id string) {
	raw, _ := json.Marshal(in)
	st.Workers.Idempotency[key] = domain.WorkerIdempotency{Digest: workerartifact.BytesDigest(raw), ID: id}
}
