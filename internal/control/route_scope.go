package control

import "net/http"

// readScopeMode is declared per route at registration time. The storage layer
// still makes authorization decisions from the loaded rows; this mode only
// bounds which rows a request loads.
type readScopeMode uint8

const (
	scopeUnset readScopeMode = iota
	// scopeNone deliberately loads the requested partitions without filtering.
	scopeNone
	// scopeActor loads the actor's own users, groups and grants.
	scopeActor
	// scopeActorAllOrganizations is scopeActor plus every organization the
	// actor can reach; it is used by the organization catalog.
	scopeActorAllOrganizations
	// scopeActorAudit is scopeActor without the shared pagination parser.
	scopeActorAudit
	// scopeOrganization bounds workers and IAM rows to one organization.
	scopeOrganization
	// scopeOrganizationWide loads every user, group and grant in one
	// organization for its administration lists.
	scopeOrganizationWide
	// scopeWorker bounds workers and subordinate rows to one Worker.
	scopeWorker
	// scopeWorkerSummary bounds the Worker list and its search/cursor options.
	scopeWorkerSummary
	// scopeCloudflareOrganization is the Cloudflare API's organization scope.
	// Its list handlers own pagination and need full organization collections.
	scopeCloudflareOrganization
	// scopeCloudflareAccounts is the Cloudflare account catalog scope.
	scopeCloudflareAccounts
)

type recordingMux struct {
	*http.ServeMux

	scopes  map[string]readScopeMode
	current readScopeMode
}

func newRecordingMux() *recordingMux {
	return &recordingMux{
		ServeMux: http.NewServeMux(),
		scopes:   map[string]readScopeMode{},
	}
}

// useScope applies to every route registered after it.
func (m *recordingMux) useScope(mode readScopeMode) {
	m.current = mode
}

func (m *recordingMux) HandleFunc(pattern string, handler http.HandlerFunc) {
	m.record(pattern)
	m.ServeMux.HandleFunc(pattern, handler)
}

func (m *recordingMux) Handle(pattern string, handler http.Handler) {
	m.record(pattern)
	m.ServeMux.Handle(pattern, handler)
}

func (m *recordingMux) record(pattern string) {
	if m.current == scopeUnset {
		panic("route " + pattern + " does not declare a read scope")
	}
	m.scopes[pattern] = m.current
}
