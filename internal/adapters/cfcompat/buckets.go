package cfcompat

import (
	"net/http"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
)

func (s *Server) cfBucketList(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	if jurisdiction := r.Header.Get("cf-r2-jurisdiction"); jurisdiction != "" && jurisdiction != "default" {
		return nil, 0, domain.Problem(422, "UNSUPPORTED_STORAGE_OPTION", "R2 jurisdictions are not supported")
	}
	out, err := s.Application(r).BucketList(st, p, r.PathValue("org"))
	if err != nil {
		return nil, 0, err
	}
	buckets := []any{}
	for _, bucket := range out {
		value := map[string]any{"name": bucket.Name, "creation_date": bucket.Created, "location": "auto", "storage_class": "Standard"}
		if r.PathValue("bucket") != "" {
			if bucket.Name == r.PathValue("bucket") && bucket.Enabled {
				return value, 200, nil
			}
			continue
		}
		if bucket.Enabled {
			buckets = append(buckets, value)
		}
	}
	if r.PathValue("bucket") != "" {
		return nil, 0, domain.Problem(404, "STORAGE_BUCKET_NOT_FOUND", "R2 bucket not found")
	}
	return map[string]any{"buckets": buckets}, 200, nil
}

func (s *Server) cfBucketCreate(st *domain.State, p authorization.Principal, r *http.Request) (any, int, error) {
	if jurisdiction := r.Header.Get("cf-r2-jurisdiction"); jurisdiction != "" && jurisdiction != "default" {
		return nil, 0, domain.Problem(422, "UNSUPPORTED_STORAGE_OPTION", "R2 jurisdictions are not supported")
	}
	var input struct {
		Name         string `json:"name"`
		LocationHint string `json:"locationHint,omitempty"`
		StorageClass string `json:"storageClass,omitempty"`
	}
	if err := s.body(r, &input); err != nil {
		return nil, 0, err
	}
	if input.LocationHint != "" || input.StorageClass != "" && input.StorageClass != "Standard" {
		return nil, 0, domain.Problem(422, "UNSUPPORTED_STORAGE_OPTION", "Bucket placement and storage class are deployment configured")
	}
	out, err := s.Application(r).BucketCreate(st, p, r.PathValue("org"), input.Name)
	return map[string]any{"name": out.Name, "creation_date": out.Created, "location": "auto", "storage_class": "Standard"}, 200, err
}
