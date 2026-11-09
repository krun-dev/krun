package workers

import (
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
)

// AuthorizeBucket returns a request-local grant, before contacting object storage.
// Administrative bucket visibility does not confer access to its contents.
func (s *Service) AuthorizeBucket(st *domain.State, p authorization.Principal, organization, id, action string, byName bool) (domain.WorkerBucket, error) {
	switch action {
	case "storage.object.read", "storage.object.write", "storage.object.delete":
	default:
		return domain.WorkerBucket{}, domain.Forbidden()
	}
	target, err := s.orgTarget(st, WorkerRef{Organization: organization})
	if err != nil {
		return domain.WorkerBucket{}, err
	}
	if err := s.require(st, p, action, target); err != nil {
		return domain.WorkerBucket{}, err
	}
	if s.Config.Workers == nil || s.Config.Workers.ObjectStorage == nil {
		return domain.WorkerBucket{}, domain.Problem(503, "OBJECT_STORAGE_NOT_CONFIGURED", "Object storage is not configured")
	}
	bucket, exists := findBucket(st, organization, id, byName)
	if !exists || !bucket.Enabled {
		return domain.WorkerBucket{}, domain.NotFound()
	}
	return bucket, nil
}
