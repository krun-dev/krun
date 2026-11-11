package workers

import (
	"regexp"
	"sort"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func (s *Service) BucketList(st *domain.State, p authorization.Principal, organization string) ([]domain.WorkerBucket, error) {
	target, err := s.orgTarget(st, WorkerRef{Organization: organization})
	if err != nil {
		return nil, err
	}
	if err := s.require(st, p, "organization.read", target); err != nil {
		return nil, err
	}
	out := []domain.WorkerBucket{}
	for _, bucket := range st.Workers.Buckets {
		if bucket.Organization == organization {
			out = append(out, bucket)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Service) BucketGet(st *domain.State, p authorization.Principal, organization, id string) (domain.WorkerBucket, error) {
	target, err := s.orgTarget(st, WorkerRef{Organization: organization})
	if err != nil {
		return domain.WorkerBucket{}, err
	}
	if err := s.require(st, p, "organization.read", target); err != nil {
		return domain.WorkerBucket{}, err
	}
	bucket, exists := findBucket(st, organization, id, false)
	if !exists {
		return domain.WorkerBucket{}, domain.NotFound()
	}
	return bucket, nil
}

// findBucket checks the organization for both native references and Cloudflare names.
func findBucket(st *domain.State, org, ref string, nameOnly bool) (domain.WorkerBucket, bool) {
	if bucket, ok := st.Workers.Buckets[ref]; !nameOnly && ok && bucket.Organization == org {
		return bucket, true
	}
	for _, bucket := range st.Workers.Buckets {
		if bucket.Organization == org && bucket.Name == ref {
			return bucket, true
		}
	}
	return domain.WorkerBucket{}, false
}

func (s *Service) BucketCreate(st *domain.State, p authorization.Principal, organization, name string) (domain.WorkerBucket, error) {
	target, err := s.orgTarget(st, WorkerRef{Organization: organization})
	if err != nil {
		return domain.WorkerBucket{}, err
	}
	if err := s.require(st, p, "storage.bucket.create", target); err != nil {
		return domain.WorkerBucket{}, err
	}
	if s.Config.Workers == nil || s.Config.Workers.ObjectStorage == nil {
		return domain.WorkerBucket{}, domain.Unavailable("OBJECT_STORAGE_NOT_CONFIGURED", "Object storage is not configured")
	}
	if !bucketNamePattern.MatchString(name) {
		return domain.WorkerBucket{}, domain.Invalid("BUCKET_NAME_INVALID", "Invalid bucket name")
	}
	count := 0
	for _, bucket := range st.Workers.Buckets {
		if bucket.Organization != organization {
			continue
		}
		count++
		if bucket.Name == name {
			return domain.WorkerBucket{}, domain.Conflict("BUCKET_ALREADY_EXISTS", "Bucket already exists")
		}
	}
	if count >= 1000 {
		return domain.WorkerBucket{}, domain.Conflict("BUCKET_LIMIT_REACHED", "Organization bucket limit reached")
	}
	bucket := domain.WorkerBucket{
		ID:           securetoken.NewID("bkt"),
		Organization: organization,
		Name:         name,
		Store:        s.Config.Workers.ObjectStorage.DefaultStore,
		Enabled:      true,
		Revision:     1,
		Created:      time.Now().UTC().Truncate(time.Microsecond),
	}
	st.Workers.Buckets[bucket.ID] = bucket
	s.audit(st, p, "workers.bucket.create", bucket.ID, true)
	return bucket, nil
}

func (s *Service) BucketSetEnabled(st *domain.State, p authorization.Principal, organization, id string, enabled bool, revision int64) (domain.WorkerBucket, error) {
	target, err := s.orgTarget(st, WorkerRef{Organization: organization})
	if err != nil {
		return domain.WorkerBucket{}, err
	}
	if err := s.require(st, p, "storage.bucket.update", target); err != nil {
		return domain.WorkerBucket{}, err
	}
	bucket, exists := findBucket(st, organization, id, false)
	if !exists {
		return domain.WorkerBucket{}, domain.NotFound()
	}
	if bucket.Revision != revision {
		return domain.WorkerBucket{}, domain.Conflict("REVISION_CONFLICT", "Bucket changed")
	}
	bucket.Enabled = enabled
	bucket.Revision++
	st.Workers.Buckets[bucket.ID] = bucket
	s.audit(st, p, "workers.bucket.update", bucket.ID, true)
	return bucket, nil
}

func resolveR2Bindings(st *domain.State, organization string, requested map[string]string) (map[string]domain.WorkerR2Binding, error) {
	if len(requested) > 100 {
		return nil, domain.Invalid("WORKER_BINDINGS_INVALID", "At most 100 R2 bindings")
	}
	out := make(map[string]domain.WorkerR2Binding, len(requested))
	for name, bucketName := range requested {
		found := false
		for _, bucket := range st.Workers.Buckets {
			if bucket.Organization == organization && bucket.Name == bucketName && bucket.Enabled {
				out[name] = domain.WorkerR2Binding{StoreID: bucket.Store, BucketID: bucket.ID, BucketName: bucket.Name}
				found = true
				break
			}
		}
		if !found {
			return nil, domain.Invalid("STORAGE_BUCKET_UNAVAILABLE", "R2 bucket does not exist or is disabled in this organization")
		}
	}
	return out, nil
}
