package control

import (
	"net/http/httptest"
	"testing"
)

func TestRoutesDeclareReadScope(t *testing.T) {
	s, _ := testServer(t)

	if len(s.routeScopes) < 100 {
		t.Fatalf("only %d routes declared a read scope", len(s.routeScopes))
	}
	for pattern, mode := range s.routeScopes {
		if mode == scopeUnset {
			t.Errorf("route %s has no read scope", pattern)
		}
	}
	for pattern, want := range map[string]readScopeMode{
		"GET /api/auth/me":                          scopeActor,
		"GET /api/v1/orgs":                          scopeActorAllOrganizations,
		"GET /api/v1/orgs/{org}/workers":            scopeWorkerSummary,
		"GET /api/v1/orgs/{org}/workers/{resource}": scopeWorker,
		"GET /api/v1/orgs/{org}/buckets":            scopeOrganization,
		"GET /api/v1/orgs/{org}/hyperdrive":         scopeOrganization,
		"GET /api/v1/system/users":                  scopeNone,
		"GET /api/v1/system/audit":                  scopeActorAudit,
		"GET /api/v1/cloudflare/client/v4/accounts/{account}/workers/scripts/{script}/versions/{version}": scopeCloudflareOrganization,
	} {
		if got, ok := s.routeScopes[pattern]; !ok || got != want {
			t.Errorf("route %s scope = %v, want %v (registered=%v)", pattern, got, want, ok)
		}
	}
}

func TestOrganizationScopeIsAppliedToBucketRoutes(t *testing.T) {
	s, _ := testServer(t)

	request := httptest.NewRequest("GET", "/api/v1/orgs/org-a/buckets", nil)
	request.Pattern = "GET /api/v1/orgs/{org}/buckets"
	request.SetPathValue("org", "org-a")
	request, err := s.scopedRequest(request, authCredentials{session: Session{User: "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	scope, ok := request.Context().Value(stateReadScopeKey{}).(stateReadScope)
	if !ok {
		t.Fatal("bucket route did not receive a read scope")
	}
	if scope.Organization != "org-a" || scope.ActorOnly || scope.AllOrganizations || !scope.Summary {
		t.Fatalf("bucket route scope = %+v", scope)
	}
}
