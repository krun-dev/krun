package control

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

// Resolve only after authentication, authorization and state persistence finish.
// Network calls must run outside the database transaction.
type deferredResponse struct {
	run func(context.Context) (any, int, error)
}

type objectRedirect string

func (s *Server) workerObjects(st *State, p Principal, r *http.Request) (any, int, error) {
	action := "storage.object.read"
	var access domain.BucketObjectAccessInput
	if r.Method == http.MethodDelete {
		action = "storage.object.delete"
	} else if r.Method == http.MethodPost {
		if err := body(r, &access); err != nil {
			return nil, 0, err
		}
		if access.Operation == "upload" {
			action = "storage.object.write"
		}
	}
	byName := r.PathValue("account") != ""
	if byName {
		if jurisdiction := r.Header.Get("cf-r2-jurisdiction"); jurisdiction != "" && jurisdiction != "default" {
			return nil, 0, problem(422, "UNSUPPORTED_STORAGE_OPTION", "R2 jurisdictions are not supported")
		}
	}
	bucket, err := s.workerApplication(r).AuthorizeBucket(st, p, r.PathValue("org"), r.PathValue("bucket"), action, byName)
	if err != nil {
		return nil, 0, err
	}
	if s.bucketObjects == nil {
		return nil, 0, problem(503, "OBJECT_STORAGE_NOT_CONFIGURED", "Object storage is not configured")
	}
	key := r.URL.Query().Get("key")
	list := domain.BucketObjectList{
		Prefix:    r.URL.Query().Get("prefix"),
		Cursor:    r.URL.Query().Get("cursor"),
		Limit:     100,
		Delimiter: "/",
	}
	limitName := "limit"
	if byName {
		limitName = "per_page"
		list.Delimiter = r.URL.Query().Get("delimiter")
		list.StartAfter = r.URL.Query().Get("start_after")
	}
	if value := r.URL.Query().Get(limitName); value != "" {
		list.Limit, err = strconv.Atoi(value)
		if err != nil {
			return nil, 0, problem(422, "INVALID_REQUEST", "Invalid object prefix or page size")
		}
	}
	operation := r.PathValue("objectOperation")
	return deferredResponse{run: func(parent context.Context) (any, int, error) {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		switch {
		case r.Method == http.MethodDelete:
			return map[string]bool{"deleted": true}, 200, s.bucketObjects.Delete(ctx, bucket, key)
		case r.Method == http.MethodPost:
			out, err := s.bucketObjects.Access(ctx, bucket, access)
			return out, 200, err
		case operation == "head":
			out, err := s.bucketObjects.Head(ctx, bucket, key)
			return out, 200, err
		case operation == "download":
			out, err := s.bucketObjects.Access(ctx, bucket, domain.BucketObjectAccessInput{Key: key, Operation: "download"})
			return objectRedirect(out.URL), 302, err
		case operation != "":
			return nil, 0, domain.NotFound()
		default:
			out, err := s.bucketObjects.List(ctx, bucket, list)
			return out, 200, err
		}
	}}, 200, nil
}
