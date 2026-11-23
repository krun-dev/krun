package control

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/runtimeconfig"
	"github.com/krun-dev/krun/internal/testpgx"
)

// An opt-in real database workload. Versions are real source rows rather than
// a map returned by a mock; this measures metadata only, excluding artifact I/O.
func TestPostgresRuntimeResourcesTenThousandActive(t *testing.T) {
	if os.Getenv("KRUN_RESOURCE_SCALE") != "1" {
		t.Skip("set KRUN_RESOURCE_SCALE=1 for 100k sites / 10k active versions")
	}
	schema := testpgx.NewSchema(t, "krun_resources_scale")
	ctx := context.Background()
	store, err := openStoreForTest(t, ctx, schema.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Now()
	seedPerformanceSites(t, store, 100000)
	for _, query := range []string{
		`INSERT INTO krun_worker_versions(version_id,org_id,worker_id,env_name,artifact_id) SELECT 'version-'||worker_id,org_id,worker_id,'production','artifact-'||worker_id FROM krun_workers ORDER BY worker_id LIMIT 10000`,
		`UPDATE krun_worker_environments e SET version_id=v.version_id FROM krun_worker_versions v WHERE v.worker_id=e.worker_id AND e.env_name='production'`,
		`INSERT INTO worker_runtime_bindings(host,route_key,pool,worker_id,env_name,match_type,path,enabled,public,script_name) SELECT worker_id||'.workers.test','','default',worker_id,'production','','/',true,true,worker_id FROM krun_workers`,
	} {
		if _, err = postgresTestPool(store).Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = postgresTestPool(store).Exec(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded 100000 sites, 10000 active versions and bindings in %s", time.Since(started))
	hub := runtimeconfig.NewResourceHub(store, appconfig.WorkerRuntimeConfig{})
	const batchSize = 4
	jobs := make(chan []domain.ResourceRef, 2500)
	for start := 1; start <= 10000; start += batchSize {
		refs := []domain.ResourceRef{}
		for i := start; i < start+batchSize && i <= 10000; i++ {
			refs = append(refs, domain.ResourceRef{Kind: "route", ID: fmt.Sprintf("default/site-%06d.workers.test", i)})
		}
		jobs <- refs
	}
	close(jobs)
	type result struct {
		elapsed time.Duration
		err     error
	}
	results := make(chan result, 2500)
	var group sync.WaitGroup
	started = time.Now()
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for refs := range jobs {
				at := time.Now()
				_, resources, err := hub.Resources(ctx, "default", refs)
				if err == nil {
					versions := 0
					all := make([]domain.ResourceRef, 0, len(resources))
					for _, r := range resources {
						all = append(all, r.ResourceRef)
						if r.Kind == "version" {
							versions++
						}
					}
					if versions != len(refs) {
						err = fmt.Errorf("wanted %d actual Version rows, got %d", len(refs), versions)
					}
					hub.Watch("default", all, make(chan struct{}, 1))
				}
				results <- result{time.Since(at), err}
			}
		}()
	}
	group.Wait()
	close(results)
	elapsed := time.Since(started)
	samples := []time.Duration{}
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		samples = append(samples, r.elapsed)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	if err = hub.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	before := hub.Stats()["source_loads"]
	started = time.Now()
	for i := 1; i <= 10000; i++ {
		if _, _, err = hub.Resources(ctx, "default", []domain.ResourceRef{{Kind: "route", ID: fmt.Sprintf("default/site-%06d.workers.test", i)}}); err != nil {
			t.Fatal(err)
		}
	}
	after := hub.Stats()
	if after["source_loads"] != before {
		t.Fatalf("hot requests reloaded source: %d -> %d", before, after["source_loads"])
	}
	t.Logf("10000 cold hosts: %s, 4 concurrent readers, batches<=4, batch p50=%s p95=%s; 10000 hot graph reads=%s; stats=%v", elapsed, samples[len(samples)/2], samples[len(samples)*95/100], time.Since(started), after)
}
