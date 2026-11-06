package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestWorkerSearchPaginationAndCreationTime(t *testing.T) {
	s, store, _ := cfTestServer(t)
	created := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
	if err := store.Update(context.Background(), PartitionAll, func(st *State) error {
		for i := 0; i < 5; i++ {
			id := fmt.Sprintf("list-%02d", i)
			st.Resources[id] = Resource{ID: id, Kind: "worker", Organization: "org-a", Name: "Search Demo", Revision: 1, Created: created}
		}
		st.Resources["list-other"] = Resource{ID: "list-other", Kind: "worker", Organization: "org-b", Name: "Search Demo", Revision: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/orgs/org-a/workers"
	var page struct {
		Items     []workerapp.WorkerSummary `json:"items"`
		NextAfter string                    `json:"next_after"`
	}
	seen := map[string]bool{}
	after := ""
	for _, count := range []int{2, 2, 1} {
		response := request(s, "GET", base+"?q=sEaRcH&limit=2&after="+url.QueryEscape(after), "krun_cli_admin", "")
		page.NextAfter = ""
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Items) != count {
			t.Fatalf("page: %d %s (%v)", response.Code, response.Body.String(), err)
		}
		for _, item := range page.Items {
			if seen[item.ID] || item.Organization != "org-a" || !item.Created.Equal(created) {
				t.Fatalf("invalid summary: %+v", item)
			}
			seen[item.ID] = true
		}
		after = page.NextAfter
	}
	if after != "" || len(seen) != 5 {
		t.Fatalf("incorrect terminal page: %q, %v", after, seen)
	}
	for _, query := range []string{"q=LIST-04", "worker_id=list-04&q=missing&after=zzz"} {
		response := request(s, "GET", base+"?"+query, "krun_cli_admin", "")
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != "list-04" {
			t.Fatalf("detail/search: %d %s", response.Code, response.Body.String())
		}
	}
	for _, limit := range []string{"0", "201", "bad"} {
		if response := request(s, "GET", base+"?limit="+limit, "krun_cli_admin", ""); response.Code != 422 {
			t.Fatalf("invalid limit accepted: %d %s", response.Code, response.Body.String())
		}
	}
	response := request(s, "GET", base+"?q=missing", "krun_cli_admin", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatal(response.Code, response.Body.String())
	}

	response = request(s, "POST", "/api/v1/orgs/org-a/workers", "krun_cli_admin", `{"name":"Fresh Worker"}`)
	var resource Resource
	if err := json.Unmarshal(response.Body.Bytes(), &resource); err != nil || response.Code != 201 || resource.Created.IsZero() {
		t.Fatalf("creation time missing: %d %s", response.Code, response.Body.String())
	}
	response = request(s, "PATCH", "/api/v1/orgs/org-a/workers/"+resource.ID, "krun_cli_admin", `{"name":"Renamed Worker","expected_revision":1}`)
	var renamed Resource
	if err := json.Unmarshal(response.Body.Bytes(), &renamed); err != nil || response.Code != 200 || !renamed.Created.Equal(resource.Created) {
		t.Fatalf("creation time changed on rename: %d %s", response.Code, response.Body.String())
	}
	// The CF script registry and envelope remain independent of console paging.
	response = request(s, "GET", cfPrefix+"/accounts/org-a/workers/scripts?q=missing&limit=1", "krun_cli_admin", "")
	var cf struct {
		Success bool                  `json:"success"`
		Result  []struct{ ID string } `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &cf); err != nil || !cf.Success || len(cf.Result) != 1 || cf.Result[0].ID != "fixture" {
		t.Fatalf("CF scripts changed: %d %s", response.Code, response.Body.String())
	}
}

func TestBucketSearchPaginationDetailsAndCFCollection(t *testing.T) {
	s, store, _ := cfTestServer(t)
	created := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
	if err := store.Update(context.Background(), PartitionWorkers, func(st *State) error {
		for i := 0; i < 205; i++ {
			id := fmt.Sprintf("bucket-%03d", i)
			st.Workers.Buckets[id] = domain.WorkerBucket{ID: id, Organization: "org-a", Name: fmt.Sprintf("images-%03d", i), Enabled: i != 0, Revision: 1, Created: created}
		}
		st.Workers.Buckets["bucket-other"] = domain.WorkerBucket{ID: "bucket-other", Organization: "org-b", Name: "images-other", Enabled: true, Created: created}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/orgs/org-a/buckets"
	var page struct {
		Items []domain.WorkerBucket `json:"items"`
		Next  string                `json:"next_after"`
	}
	seen := map[string]bool{}
	after := ""
	for _, count := range []int{100, 100, 5} {
		response := request(s, "GET", base+"?limit=100&after="+url.QueryEscape(after), "krun_cli_admin", "")
		page.Next = ""
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Items) != count {
			t.Fatalf("page: %d %s", response.Code, response.Body.String())
		}
		for _, bucket := range page.Items {
			if seen[bucket.ID] || bucket.Organization != "org-a" {
				t.Fatalf("invalid bucket: %+v", bucket)
			}
			seen[bucket.ID] = true
		}
		after = page.Next
	}
	if after != "" || len(seen) != 205 {
		t.Fatalf("bucket pagination lost results: %q %d", after, len(seen))
	}
	for _, query := range []string{"q=IMAGES-204", "q=BUCKET-204"} {
		response := request(s, "GET", base+"?limit=20&"+query, "krun_cli_admin", "")
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != "bucket-204" {
			t.Fatalf("search: %d %s", response.Code, response.Body.String())
		}
	}
	for _, id := range []string{"bucket-000", "bucket-204"} {
		response := request(s, "GET", base+"/"+id, "krun_cli_admin", "")
		var bucket domain.WorkerBucket
		if err := json.Unmarshal(response.Body.Bytes(), &bucket); err != nil || response.Code != 200 || bucket.ID != id || !bucket.Created.Equal(created) {
			t.Fatalf("bucket detail: %d %s", response.Code, response.Body.String())
		}
	}
	if response := request(s, "GET", base+"/bucket-other", "krun_cli_admin", ""); response.Code != 404 {
		t.Fatal("cross-organization detail exposed", response.Code, response.Body.String())
	}
	if response := request(s, "GET", base+"?limit=0", "krun_cli_admin", ""); response.Code != 422 {
		t.Fatal("invalid bucket limit accepted", response.Code, response.Body.String())
	}
	// Selectors retain the complete name-ordered list without pagination params.
	response := request(s, "GET", base, "krun_cli_admin", "")
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Items) != 205 || page.Items[0].Name != "images-000" {
		t.Fatalf("binding collection truncated: %d %s", response.Code, response.Body.String())
	}
	response = request(s, "GET", cfPrefix+"/accounts/org-a/r2/buckets?q=missing&limit=1&after=zzz", "krun_cli_admin", "")
	var cf struct {
		Success bool `json:"success"`
		Result  struct {
			Buckets []struct {
				Name         string    `json:"name"`
				CreationDate time.Time `json:"creation_date"`
			} `json:"buckets"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &cf); err != nil || !cf.Success || len(cf.Result.Buckets) != 204 || cf.Result.Buckets[0].Name != "images-001" || !cf.Result.Buckets[0].CreationDate.Equal(created) {
		t.Fatalf("CF bucket collection changed: %d %s", response.Code, response.Body.String())
	}
}
