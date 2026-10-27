package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"
)

func seedAuditPages(t *testing.T, store *Store) {
	t.Helper()
	at := now().Add(-time.Hour)
	store.memoryAudits = nil
	err := store.Update(context.Background(), PartitionAll, func(st *State) error {
		for i := 0; i < 237; i++ {
			// Equal times deliberately exercise the ID tie breaker.
			st.recordAudit(Audit{ID: fmt.Sprintf("entry-%04d", i), Time: at.Add(time.Duration(i/3) * time.Second), Organization: "org-a", Actor: "admin", Action: "service.read", Resource: "svc-a", Allowed: true})
		}
		st.recordAudit(Audit{ID: "other-org", Time: at, Organization: "org-b", Resource: "svc-b"})
		st.recordAudit(Audit{ID: "old", Time: now().Add(-8 * 24 * time.Hour), Organization: "org-a"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func getAuditPage(t *testing.T, s *Server, path, token string) auditPage {
	t.Helper()
	w := request(s, "GET", path, token, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page auditPage
	if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil {
		t.Fatal(e)
	}
	return page
}

func TestAuditPaginationSnapshotAndScope(t *testing.T) {
	s, store := testServer(t)
	seedAuditPages(t, store)
	path := "/api/v1/orgs/org-a/audit?limit=50"
	first := getAuditPage(t, s, path, "krun_cli_admin")
	if first.Total != 237 || len(first.Items) != 50 || !first.HasMore || first.Cursor == "" || first.NextCursor == "" || first.RetentionDays != 7 {
		t.Fatalf("bad first page %+v", first)
	}
	// Live traffic and pagination requests cannot shift the snapshot.
	_ = store.Update(context.Background(), PartitionAll, func(st *State) error {
		st.recordAudit(Audit{ID: "new", Time: now(), Organization: "org-a"})
		return nil
	})
	seen := map[string]bool{}
	page := first
	var previous *Audit
	for {
		if page.Total != 237 || !page.AsOf.Equal(first.AsOf) {
			t.Fatal("snapshot changed between pages")
		}
		for _, entry := range page.Items {
			if seen[entry.ID] || entry.Organization != "org-a" || entry.ID == "new" || entry.ID == "old" {
				t.Fatal("duplicate or wrong scope", entry.ID)
			}
			if previous != nil && (entry.Time.After(previous.Time) || (entry.Time.Equal(previous.Time) && entry.ID >= previous.ID)) {
				t.Fatal("unstable sort")
			}
			seen[entry.ID] = true
			copy := entry
			previous = &copy
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatal("final page has cursor")
			}
			break
		}
		page = getAuditPage(t, s, path+"&cursor="+url.QueryEscape(page.NextCursor), "krun_cli_admin")
	}
	if len(seen) != 237 || len(page.Items) != 37 {
		t.Fatal("records beyond 200 were not fully pageable", len(seen), len(page.Items))
	}
	back := getAuditPage(t, s, path+"&cursor="+url.QueryEscape(first.Cursor), "krun_cli_admin")
	if len(back.Items) != 50 || back.Items[0].ID != first.Items[0].ID || back.Total != first.Total {
		t.Fatal("back navigation lost snapshot")
	}
	fresh := getAuditPage(t, s, path, "krun_cli_admin")
	if fresh.Total <= first.Total || fresh.Items[0].Time.Before(first.AsOf) {
		t.Fatal("refresh did not include new traffic")
	}
	large := getAuditPage(t, s, "/api/v1/orgs/org-a/audit?limit=500&cursor="+url.QueryEscape(first.Cursor), "krun_cli_admin")
	if len(large.Items) != 237 || large.HasMore {
		t.Fatal("larger page did not expose more records")
	}
	if w := request(s, "GET", "/api/v1/orgs/org-b/audit?cursor="+url.QueryEscape(first.NextCursor), "krun_cli_admin", ""); w.Code != 400 {
		t.Fatal("cross-scope cursor accepted", w.Code)
	}
	if w := request(s, "GET", path+"&cursor="+url.QueryEscape(first.NextCursor), "krun_cli_viewer", ""); w.Code != 403 {
		t.Fatal("cursor bypassed user permissions", w.Code)
	}
	global := getAuditPage(t, s, "/api/v1/system/audit?limit=500", "krun_cli_admin")
	found := false
	for _, entry := range global.Items {
		found = found || entry.ID == "other-org"
		if entry.ID == "old" {
			t.Fatal("old audit visible")
		}
	}
	if !found {
		t.Fatal("system audit missing other organization")
	}
}

func TestAuditPaginationValidation(t *testing.T) {
	s, _ := testServer(t)
	for _, query := range []string{"limit=0", "limit=-1", "limit=501", "limit=bad", "limit=", "limit=1&limit=2", "cursor=bad", "cursor=x&cursor=y"} {
		w := request(s, "GET", "/api/v1/system/audit?"+query, "krun_cli_admin", "")
		if w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	cursor := s.encodeAuditCursor(auditCursor{Scope: "system:system", AsOf: now().Add(-8 * 24 * time.Hour)})
	if w := request(s, "GET", "/api/v1/system/audit?cursor="+url.QueryEscape(cursor), "krun_cli_admin", ""); w.Code != 409 {
		t.Fatal("expired cursor accepted", w.Code)
	}
	future := s.encodeAuditCursor(auditCursor{Scope: "system:system", AsOf: now().Add(time.Hour)})
	if w := request(s, "GET", "/api/v1/system/audit?cursor="+url.QueryEscape(future), "krun_cli_admin", ""); w.Code != 400 {
		t.Fatal("future cursor accepted")
	}
	empty := getAuditPage(t, s, "/api/v1/system/audit", "krun_cli_admin")
	if empty.Limit != 50 {
		t.Fatal("wrong default page size")
	}
}

func TestAuditRetentionBoundary(t *testing.T) {
	at := now()
	cutoff := at.Add(-auditRetention)
	kept := pruneMemoryAudits([]Audit{{ID: "expired", Time: cutoff.Add(-time.Nanosecond)}, {ID: "boundary", Time: cutoff}, {ID: "recent", Time: at}}, at)
	if len(kept) != 2 || kept[0].ID != "boundary" || kept[1].ID != "recent" {
		t.Fatal("wrong retention boundary")
	}
}

func TestAuditRetentionDuringIdleTime(t *testing.T) {
	store := memoryStore()
	store.memoryAudits = []Audit{{ID: "expired", Time: now().Add(-8 * 24 * time.Hour)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); store.retainAudits(ctx, time.Millisecond) }()
	defer func() { cancel(); <-done }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("idle retention did not delete expired data")
		case <-ticker.C:
			store.mu.Lock()
			count := len(store.memoryAudits)
			store.mu.Unlock()
			if count == 0 {
				return
			}
		}
	}
}
