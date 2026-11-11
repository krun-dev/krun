package control

import (
	"net/http"
	"sort"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/resourcename"
)

func (s *Server) workerBucketList(st *State, p Principal, r *http.Request) (any, int, error) {
	query, err := workerListOptions(r)
	if err != nil {
		return nil, 0, err
	}
	buckets, err := s.workerApplication(r).BucketList(st, p, r.PathValue("org"))
	if err != nil {
		return nil, 0, err
	}
	for i := range buckets {
		buckets[i].ResourcePath = resourcePath(st, buckets[i].Organization, "buckets", buckets[i].Name)
	}
	// Binding selectors use the unpaginated, name-ordered collection. Only
	// console list requests opt into pagination; CF uses its own handler.
	if !r.URL.Query().Has("limit") && !r.URL.Query().Has("after") && !r.URL.Query().Has("q") {
		return map[string]any{"items": buckets}, 200, nil
	}
	items := []domain.WorkerBucket{}
	for _, bucket := range buckets {
		if query.matches(bucket.ID, bucket.Name) {
			items = append(items, bucket)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	out := map[string]any{}
	if len(items) > query.limit {
		items = items[:query.limit]
		out["next_after"] = items[len(items)-1].ID
	}
	out["items"] = items
	return out, 200, nil
}

func (s *Server) workerBucketGet(st *State, p Principal, r *http.Request) (any, int, error) {
	out, err := s.workerApplication(r).BucketGet(st, p, r.PathValue("org"), r.PathValue("bucket"))
	out.ResourcePath = resourcePath(st, out.Organization, "buckets", out.Name)
	return out, 200, err
}

func (s *Server) workerBucketCreate(st *State, p Principal, r *http.Request) (any, int, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := body(r, &input); err != nil {
		return nil, 0, err
	}
	name, err := resourcename.Make(input.Name, 3, resourcename.ResourceMax)
	if err != nil {
		return nil, 0, err
	}
	out, err := s.workerApplication(r).BucketCreate(st, p, r.PathValue("org"), name)
	out.ResourcePath = resourcePath(st, out.Organization, "buckets", out.Name)
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
	out.ResourcePath = resourcePath(st, out.Organization, "buckets", out.Name)
	return out, 200, err
}
