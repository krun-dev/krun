package control

import "net/http"

func (s *Server) workerBucketList(st *State, p Principal, r *http.Request) (any, int, error) {
	out, err := s.workerApplication(r).BucketList(st, p, r.PathValue("org"))
	return map[string]any{"items": out}, 200, err
}

func (s *Server) workerBucketCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := body(r, &input); err != nil {
		return nil, 0, err
	}
	out, err := s.workerApplication(r).BucketCreate(st, p, r.PathValue("org"), input.Name)
	return out, 201, err
}

func (s *Server) workerBucketUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	var input struct {
		Enabled  *bool `json:"enabled"`
		Revision int64 `json:"revision"`
	}
	if err := body(r, &input); err != nil {
		return nil, 0, err
	}
	if input.Enabled == nil {
		return nil, 0, problem(422, "INVALID_REQUEST", "enabled is required")
	}
	out, err := s.workerApplication(r).BucketSetEnabled(st, p, r.PathValue("org"), r.PathValue("bucket"), *input.Enabled, input.Revision)
	return out, 200, err
}

func (s *Server) cfBucketList(st *State, p Principal, r *http.Request) (any, int, error) {
	if jurisdiction := r.Header.Get("cf-r2-jurisdiction"); jurisdiction != "" && jurisdiction != "default" {
		return nil, 0, problem(422, "UNSUPPORTED_STORAGE_OPTION", "R2 jurisdictions are not supported")
	}
	out, err := s.workerApplication(r).BucketList(st, p, r.PathValue("org"))
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
		return nil, 0, problem(404, "STORAGE_BUCKET_NOT_FOUND", "R2 bucket not found")
	}
	return map[string]any{"buckets": buckets}, 200, nil
}

func (s *Server) cfBucketCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	if jurisdiction := r.Header.Get("cf-r2-jurisdiction"); jurisdiction != "" && jurisdiction != "default" {
		return nil, 0, problem(422, "UNSUPPORTED_STORAGE_OPTION", "R2 jurisdictions are not supported")
	}
	var input struct {
		Name         string `json:"name"`
		LocationHint string `json:"locationHint,omitempty"`
		StorageClass string `json:"storageClass,omitempty"`
	}
	if err := body(r, &input); err != nil {
		return nil, 0, err
	}
	if input.LocationHint != "" || input.StorageClass != "" && input.StorageClass != "Standard" {
		return nil, 0, problem(422, "UNSUPPORTED_STORAGE_OPTION", "Bucket placement and storage class are deployment configured")
	}
	out, err := s.workerApplication(r).BucketCreate(st, p, r.PathValue("org"), input.Name)
	return map[string]any{"name": out.Name, "creation_date": out.Created, "location": "auto", "storage_class": "Standard"}, 200, err
}
