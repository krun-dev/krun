package domain

import (
	"errors"
	"testing"
)

func TestErrorKindsMapToHTTPStatuses(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid", Invalid("INVALID", "invalid input"), 422, "INVALID"},
		{"conflict", Conflict("REVISION_CONFLICT", "changed"), 409, "REVISION_CONFLICT"},
		{"unavailable", Unavailable("UNAVAILABLE", "not ready"), 503, "UNAVAILABLE"},
		{"forbidden", ForbiddenCode("DENIED", "denied"), 403, "DENIED"},
		{"not found", NotFoundCode("MISSING", "missing"), 404, "MISSING"},
		{"explicit", Problem(400, "BAD_REQUEST", "bad request"), 400, "BAD_REQUEST"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var problem *APIError
			if !errors.As(test.err, &problem) {
				t.Fatalf("%v is not an APIError", test.err)
			}
			if got := problem.HTTPStatus(); got != test.status {
				t.Fatalf("HTTPStatus() = %d, want %d", got, test.status)
			}
			if problem.Code != test.code || problem.Message == "" {
				t.Fatalf("error = %+v", problem)
			}
		})
	}
}

func TestForbiddenAndNotFoundKeepTheirCodes(t *testing.T) {
	var problem *APIError
	if !errors.As(Forbidden(), &problem) || problem.Code != "PERMISSION_DENIED" || problem.HTTPStatus() != 403 {
		t.Fatalf("forbidden = %+v", problem)
	}
	if !errors.As(NotFound(), &problem) || problem.Code != "RESOURCE_NOT_FOUND" || problem.HTTPStatus() != 404 {
		t.Fatalf("not found = %+v", problem)
	}
}
