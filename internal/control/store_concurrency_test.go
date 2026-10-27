package control

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func secondPostgresStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(context.Background(), os.Getenv("KRUN_TEST_POSTGRES_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestPostgresIAMRevisionCASRejectsLostUpdate(t *testing.T) {
	store := postgresTestStore(t)
	other := secondPostgresStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Resources["svc-cas"] = Resource{ID: "svc-cas", Kind: "service", Organization: "org-cas", Name: "before", Revision: 1}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for i, candidate := range []*Store{store, other} {
		i, candidate := i, candidate
		go func() {
			errs <- candidate.Update(ctx, PartitionIAM, func(st *State) error {
				resource := st.Resources["svc-cas"]
				ready <- struct{}{}
				<-release
				resource.Name = []string{"first", "second"}[i]
				resource.Revision++
				st.Resources[resource.ID] = resource
				st.Revision++
				st.recordAudit(Audit{ID: []string{"audit-cas-first", "audit-cas-second"}[i], Time: now(), Allowed: true})
				return nil
			})
		}()
	}
	<-ready
	<-ready
	close(release)

	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-errs
		if err == nil {
			succeeded++
			continue
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == "REVISION_CONFLICT" {
			conflicted++
			continue
		}
		t.Fatalf("unexpected concurrent update error: %v", err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("success=%d conflicts=%d, want one of each", succeeded, conflicted)
	}
	state, err := store.Open(ctx, PartitionIAM)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Resources["svc-cas"].Revision; got != 2 {
		t.Fatalf("resource revision=%d, want 2", got)
	}
	var audits int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_audits WHERE id IN ('audit-cas-first','audit-cas-second')`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("allowed audit was not atomic with its IAM write: got %d", audits)
	}
}

func TestPostgresOrganizationRevisionCASRejectsLostUpdate(t *testing.T) {
	store := postgresTestStore(t)
	other := secondPostgresStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["org-cas"] = Organization{ID: "org-cas", Name: "before", Sources: []string{}, Revision: 1}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for i, candidate := range []*Store{store, other} {
		i, candidate := i, candidate
		go func() {
			errs <- candidate.Update(ctx, PartitionIAM, func(st *State) error {
				organization := st.Organizations["org-cas"]
				ready <- struct{}{}
				<-release
				organization.Name = []string{"first", "second"}[i]
				organization.Revision++
				st.Organizations[organization.ID] = organization
				st.Revision++
				return nil
			})
		}()
	}
	<-ready
	<-ready
	close(release)
	assertOneRevisionConflict(t, errs)
	state, err := store.Open(ctx, PartitionIAM)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Organizations["org-cas"].Revision; got != 2 {
		t.Fatalf("organization revision=%d, want 2", got)
	}
}

func TestPostgresUserProfileCASRejectsLostUpdate(t *testing.T) {
	store := postgresTestStore(t)
	other := secondPostgresStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Users["profile-user"] = User{ID: "profile-user", Source: "source", Subject: "subject", Name: "before", Email: "before@example.com", Status: "active", AuthVersion: 1}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for i, candidate := range []*Store{store, other} {
		i, candidate := i, candidate
		go func() {
			errs <- candidate.Update(ctx, PartitionIAM, func(st *State) error {
				user := st.Users["profile-user"]
				ready <- struct{}{}
				<-release
				user.Name = []string{"first", "second"}[i]
				st.Users[user.ID] = user
				st.Revision++
				return nil
			})
		}()
	}
	<-ready
	<-ready
	close(release)
	assertOneRevisionConflict(t, errs)
}

func TestPostgresConcurrentJITIdentityIsRetryable(t *testing.T) {
	store := postgresTestStore(t)
	other := secondPostgresStore(t)
	ctx := context.Background()
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for i, candidate := range []*Store{store, other} {
		i, candidate := i, candidate
		go func() {
			errs <- candidate.Update(ctx, PartitionIAM, func(st *State) error {
				ready <- struct{}{}
				<-release
				id := []string{"jit-first", "jit-second"}[i]
				st.Users[id] = User{ID: id, Source: "jit-source", Subject: "stable-subject", Name: id, Status: "active", AuthVersion: 1}
				st.Revision++
				return nil
			})
		}()
	}
	<-ready
	<-ready
	close(release)
	assertOneRevisionConflict(t, errs)

	// The caller can retry after REVISION_CONFLICT: the fresh snapshot resolves
	// the stable identity to the row created by the winning replica.
	if err := other.Update(ctx, PartitionIAM, func(st *State) error {
		for _, user := range st.Users {
			if user.Source == "jit-source" && user.Subject == "stable-subject" {
				return nil
			}
		}
		return errors.New("stable identity was not visible on retry")
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_users WHERE source='jit-source' AND subject='stable-subject'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stable identity rows=%d, want 1", count)
	}
}

func TestPostgresIdentityRegistrationIsInsertOrVerify(t *testing.T) {
	store := postgresTestStore(t)
	other := secondPostgresStore(t)
	ctx := context.Background()
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for _, candidate := range []*Store{store, other} {
		candidate := candidate
		go func() {
			errs <- candidate.Update(ctx, PartitionIAM, func(st *State) error {
				ready <- struct{}{}
				<-release
				st.Sources["provider:company"] = "company-source"
				st.Sources["namespace:company-namespace"] = "company-source"
				st.Sources["company-source"] = "same-fingerprint"
				return nil
			})
		}()
	}
	<-ready
	<-ready
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("identical concurrent registration must be idempotent: %v", err)
		}
	}
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Sources["company-source"] = "different-fingerprint"
		return nil
	}); err == nil {
		t.Fatal("identity source reassignment succeeded")
	} else {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "REVISION_CONFLICT" {
			t.Fatalf("reassignment error=%v, want REVISION_CONFLICT", err)
		}
	}
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		delete(st.Sources, "provider:company")
		return nil
	}); err == nil {
		t.Fatal("identity registration tombstone was deleted")
	}
}

func assertOneRevisionConflict(t *testing.T, errs <-chan error) {
	t.Helper()
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-errs
		if err == nil {
			succeeded++
			continue
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == "REVISION_CONFLICT" {
			conflicted++
			continue
		}
		t.Fatalf("unexpected concurrent update error: %v", err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("success=%d conflicts=%d, want one of each", succeeded, conflicted)
	}
}

func TestOrganizationUpdateRequiresExpectedRevision(t *testing.T) {
	server, _ := testServer(t)
	first := request(server, "PATCH", "/api/v1/orgs/org-a", "krun_cli_admin", `{"name":"renamed","allowed_sources":[],"expected_revision":0}`)
	if first.Code != 200 {
		t.Fatalf("first organization update: %d %s", first.Code, first.Body.String())
	}
	stale := request(server, "PATCH", "/api/v1/orgs/org-a", "krun_cli_admin", `{"name":"stale","allowed_sources":[],"expected_revision":0}`)
	if stale.Code != 409 {
		t.Fatalf("stale organization update: %d %s", stale.Code, stale.Body.String())
	}
}

func TestPostgresIAMRevisionsAndAuditsAreRequestLocal(t *testing.T) {
	store := postgresTestStore(t)
	other := secondPostgresStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		for _, id := range []string{"svc-one", "svc-two"} {
			st.Resources[id] = Resource{ID: id, Kind: "service", Organization: "org-revision", Name: id, Revision: 1}
		}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for i, candidate := range []*Store{store, other} {
		i, candidate := i, candidate
		go func() {
			errs <- candidate.Update(ctx, PartitionIAM, func(st *State) error {
				id := []string{"svc-one", "svc-two"}[i]
				resource := st.Resources[id]
				ready <- struct{}{}
				<-release
				resource.Name += "-updated"
				resource.Revision++
				st.Resources[id] = resource
				st.Revision++
				st.recordAudit(Audit{ID: "audit-" + id, Time: now(), Allowed: true})
				return nil
			})
		}()
	}
	<-ready
	<-ready
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	rows, err := store.pool.Query(ctx, `SELECT revision FROM krun_audits WHERE id IN ('audit-svc-one','audit-svc-two') ORDER BY revision`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	revisions := []int{}
	for rows.Next() {
		var revision int
		if err = rows.Scan(&revision); err != nil {
			t.Fatal(err)
		}
		revisions = append(revisions, revision)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0] >= revisions[1] {
		t.Fatalf("audit revisions=%v, want two distinct monotonic values", revisions)
	}
	state, err := store.Open(ctx, PartitionIAM)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != revisions[1] {
		t.Fatalf("visible revision=%d, want high-water %d", state.Revision, revisions[1])
	}
}

func TestPostgresDeniedAuditSurvivesReadError(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	err := store.Mutate(ctx, PartitionIAM, 0, func(st *State) error {
		st.recordAudit(Audit{ID: "audit-denied-read", Time: now(), Allowed: false})
		return forbidden()
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("got %v, want original permission denial", err)
	}
	var count int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM krun_audits WHERE id='audit-denied-read' AND allowed=false`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("denied read audit was dropped")
	}
}

func TestPostgresAPIKeyHashLookupDoesNotHydrateIAMSnapshot(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	secret := "krun_key_only-presented-hash-is-read"
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Keys["key-direct"] = APIKey{ID: "key-direct", Owner: "owner", ScopeKind: "system", Name: "direct", Revision: 1}
		st.KeyHashes["key-direct"] = digest(secret)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, PartitionIAM)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.KeyHashes) != 0 {
		t.Fatalf("IAM snapshot loaded %d credential hashes", len(st.KeyHashes))
	}
	id, ok, err := store.apiKeyIDByHash(ctx, digest(secret))
	if err != nil || !ok || id != "key-direct" {
		t.Fatalf("lookup=(%q,%v,%v), want key-direct", id, ok, err)
	}
	if _, ok, err = store.apiKeyIDByHash(ctx, digest("krun_key_unknown")); err != nil || ok {
		t.Fatalf("unknown lookup=(%v,%v), want missing", ok, err)
	}
}

func TestPostgresDistributedRateLimitIsAtomic(t *testing.T) {
	store := postgresTestStore(t)
	other := secondPostgresStore(t)
	ctx := context.Background()
	const requests = 32
	const limit = 16
	var wg sync.WaitGroup
	results := make(chan bool, requests)
	errs := make(chan error, requests)
	for i := 0; i < requests; i++ {
		candidate := store
		if i%2 == 1 {
			candidate = other
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			exceeded, err := candidate.RateLimitExceeded(ctx, "login", "192.0.2.10", time.Minute, limit)
			results <- exceeded
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	exceeded := 0
	for result := range results {
		if result {
			exceeded++
		}
	}
	if exceeded != requests-limit {
		t.Fatalf("exceeded=%d, want %d", exceeded, requests-limit)
	}
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count FROM krun_rate_limits WHERE scope='login'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	// Once a bucket is over limit, further rejected requests must not keep
	// updating the same hot row. The durable counter saturates at limit+1.
	if count != limit+1 {
		t.Fatalf("distributed count=%d, want saturated %d", count, limit+1)
	}
}

func TestMemoryRateLimit(t *testing.T) {
	store := memoryStore()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		exceeded, err := store.RateLimitExceeded(ctx, "login", "client", time.Minute, 2)
		if err != nil || exceeded {
			t.Fatalf("request %d unexpectedly limited: exceeded=%v err=%v", i+1, exceeded, err)
		}
	}
	exceeded, err := store.RateLimitExceeded(ctx, "login", "client", time.Minute, 2)
	if err != nil || !exceeded {
		t.Fatalf("third request must be limited: exceeded=%v err=%v", exceeded, err)
	}
}

func TestStoreMigrationsAreSequential(t *testing.T) {
	for i, migration := range storeMigrations {
		if migration.version != i+1 {
			t.Fatalf("migration index %d has version %d", i, migration.version)
		}
		if migration.name == "" {
			t.Fatalf("migration %d has no stable name", migration.version)
		}
	}
	if got := itoa(storeMigrations[len(storeMigrations)-1].version); got != storeSchemaVersion {
		t.Fatalf("latest migration=%s schema version=%s", got, storeSchemaVersion)
	}
}

func TestPostgresMigrationLedgerMatchesSchemaVersion(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	rows, err := store.pool.Query(ctx, `SELECT version, name FROM krun_schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(storeMigrations) {
			t.Fatal("migration ledger contains an unsupported future version")
		}
		var version int
		var name string
		if err = rows.Scan(&version, &name); err != nil {
			t.Fatal(err)
		}
		want := storeMigrations[index]
		if version != want.version || name != want.name {
			t.Fatalf("ledger entry %d=(%d,%q), want (%d,%q)", index, version, name, want.version, want.name)
		}
		index++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(storeMigrations) {
		t.Fatalf("ledger has %d migrations, want %d", index, len(storeMigrations))
	}
	var version string
	if err = store.pool.QueryRow(ctx, `SELECT value FROM krun_meta WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != storeSchemaVersion {
		t.Fatalf("schema_version=%q, want %q", version, storeSchemaVersion)
	}
}

func TestPostgresRepositoryConnectStateConsumedOnce(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	pending := WorkerRepositoryConnectState{User: "user-one", Provider: "github", Browser: "browser-one", Expires: now().Add(time.Minute)}
	if err := store.upsertRepositoryConnectState(ctx, "oauth-state", pending); err != nil {
		t.Fatal(err)
	}
	// A callback from another browser must neither win nor destroy the valid
	// browser's one-shot state.
	if _, consumed, err := store.consumeRepositoryConnectState(ctx, "oauth-state", "wrong-browser"); err != nil || consumed {
		t.Fatalf("wrong browser consumed state: consumed=%v err=%v", consumed, err)
	}

	var winners sync.Map
	var wg sync.WaitGroup
	for _, replica := range []string{"replica-a", "replica-b"} {
		wg.Add(1)
		go func(replica string) {
			defer wg.Done()
			got, consumed, err := store.consumeRepositoryConnectState(ctx, "oauth-state", "browser-one")
			if err != nil {
				t.Error(err)
				return
			}
			if consumed {
				if got.User != pending.User || got.Provider != pending.Provider {
					t.Errorf("unexpected consumed state: %+v", got)
				}
				winners.Store(replica, true)
			}
		}(replica)
	}
	wg.Wait()
	count := 0
	winners.Range(func(any, any) bool { count++; return true })
	if count != 1 {
		t.Fatalf("expected one repository OAuth callback winner, got %d", count)
	}
}

func TestPostgresWorkersSnapshotExcludesSecretsAndBuildBlobs(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["org"] = Organization{ID: "org", Name: "Org", Revision: 1}
		st.Resources["service"] = Resource{ID: "service", Kind: "service", Organization: "org", Name: "Service", Revision: 1}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Services["service"] = WorkerService{Service: "service", Organization: "org", Environments: map[string]WorkerEnvironment{
			workerBaseline: {Name: workerBaseline, Enabled: true, ConfigRevision: 1, Variables: map[string]string{}},
		}}
		st.Workers.Builds["build-private"] = WorkerBuild{ID: "build-private", Organization: "org", Service: "service", Environment: "production", State: "queued", Created: now()}
		st.Workers.BuildPrivate["build-private"] = WorkerBuildPrivate{Actor: WorkerActor{User: "user"}, Files: map[string]string{"index.js": "secret source"}, Log: "large log"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	connection := WorkerRepositoryConnection{User: "user", Provider: "github", AccessToken: "secret-token", ConnectedAt: now()}
	if err := store.upsertRepositoryConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}

	state, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workers.BuildPrivate) != 0 || len(state.RepositoryConnections) != 0 {
		t.Fatalf("generic Workers snapshot loaded sensitive rows: private=%d connections=%d", len(state.Workers.BuildPrivate), len(state.RepositoryConnections))
	}
	private, ok, err := store.workerBuildPrivate(ctx, "build-private")
	if err != nil || !ok || private.Log != "large log" || private.Files["index.js"] != "secret source" {
		t.Fatalf("keyed private-row lookup failed: ok=%v private=%+v err=%v", ok, private, err)
	}
	got, ok, err := store.repositoryConnection(ctx, "user", "github")
	if err != nil || !ok || got.AccessToken != "secret-token" {
		t.Fatalf("keyed repository credential lookup failed: ok=%v connection=%+v err=%v", ok, got, err)
	}
}
