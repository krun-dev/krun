package control

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	auditlog "github.com/krun-dev/krun/internal/audit"
)

func seedAuditPages(t *testing.T, store *Store) {
	t.Helper()
	at := now().Add(-time.Hour)
	if err := store.DeleteAuditsBefore(context.Background(), now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	err := store.Update(context.Background(), PartitionAll, func(st *State) error {
		for i := 0; i < 237; i++ {
			// Equal times deliberately exercise the ID tie breaker.
			st.RecordAudit(Audit{ID: fmt.Sprintf("entry-%04d", i), Time: at.Add(time.Duration(i/3) * time.Second), Organization: "org-a", Actor: "admin", Action: "worker.read", Resource: "svc-a", Allowed: true})
		}
		st.RecordAudit(Audit{ID: "other-org", Time: at, Organization: "org-b", Resource: "svc-b"})
		st.RecordAudit(Audit{ID: "old", Time: now().Add(-8 * 24 * time.Hour), Organization: "org-a"})
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
		st.RecordAudit(Audit{ID: "new", Time: now(), Organization: "org-a"})
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
	cursor := auditlog.EncodeCursor(auditCursor{Scope: "system:system", AsOf: now().Add(-8 * 24 * time.Hour)})
	if w := request(s, "GET", "/api/v1/system/audit?cursor="+url.QueryEscape(cursor), "krun_cli_admin", ""); w.Code != 409 {
		t.Fatal("expired cursor accepted", w.Code)
	}
	future := auditlog.EncodeCursor(auditCursor{Scope: "system:system", AsOf: now().Add(time.Hour)})
	if w := request(s, "GET", "/api/v1/system/audit?cursor="+url.QueryEscape(future), "krun_cli_admin", ""); w.Code != 400 {
		t.Fatal("future cursor accepted")
	}
	empty := getAuditPage(t, s, "/api/v1/system/audit", "krun_cli_admin")
	if empty.Limit != 50 {
		t.Fatal("wrong default page size")
	}
}

func TestAuditCSVExportSnapshotAndRedaction(t *testing.T) {
	s, store := testServer(t)
	seedAuditPages(t, store)
	// PostgreSQL stores timestamptz with microsecond precision.
	entry := Audit{
		ID: "csv-row", Time: now().Add(-time.Minute).Truncate(time.Microsecond), Organization: "org-a",
		Actor: "  =1+1", KeyID: "key-private", Credential: "api_key",
		Action: "worker.update", Resource: "资源,\"引号\"\n下一行",
		Allowed: true, RequestID: "request-private", Revision: 7,
	}
	if err := store.AppendAudits(context.Background(), []Audit{entry}); err != nil {
		t.Fatal(err)
	}
	first := getAuditPage(t, s, "/api/v1/orgs/org-a/audit?limit=1", "krun_cli_admin")
	if err := store.AppendAudits(context.Background(), []Audit{{
		ID: "newer-than-snapshot", Time: now(), Organization: "org-a",
	}}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/orgs/org-a/audit/export?cursor=" + url.QueryEscape(first.NextCursor)

	masked := getAuditCSV(t, s, path)
	raw := getAuditCSV(t, s, path+"&redact=false")
	if len(masked) != 239 || len(raw) != len(masked) {
		t.Fatalf("export should include header and all %d snapshot records, got %d and %d", first.Total, len(masked), len(raw))
	}
	header := []string{"id", "time", "actor", "credential", "key_id", "action", "resource", "organization_id", "allowed", "request_id", "revision"}
	if !reflect.DeepEqual(raw[0], header) {
		t.Fatalf("unexpected CSV header: %q", raw[0])
	}
	at := entry.Time.UTC().Format(time.RFC3339Nano)
	wantRaw := []string{entry.ID, at, "'" + entry.Actor, entry.Credential, entry.KeyID, entry.Action, entry.Resource, entry.Organization, "true", entry.RequestID, "7"}
	wantMasked := []string{"[REDACTED]", at, "[REDACTED]", entry.Credential, "[REDACTED]", entry.Action, "[REDACTED]", "[REDACTED]", "true", "[REDACTED]", "7"}
	if !reflect.DeepEqual(raw[1], wantRaw) || !reflect.DeepEqual(masked[1], wantMasked) {
		t.Fatalf("unexpected CSV values: raw %q, masked %q", raw[1], masked[1])
	}
	for _, row := range raw[1:] {
		if row[7] != "org-a" || row[0] == "old" || row[0] == "newer-than-snapshot" {
			t.Fatalf("unexpected row in organization snapshot: %q", row)
		}
	}

	global := getAuditCSV(t, s, "/api/v1/system/audit/export?redact=false")
	if len(global) != len(raw)+2 || global[1][0] != "newer-than-snapshot" {
		t.Fatalf("system export should include the new record and the other organization, got %d rows", len(global))
	}
}

func getAuditCSV(t *testing.T, s *Server, path string) [][]string {
	t.Helper()
	w := request(s, "GET", path, "krun_cli_admin", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
		w.Header().Get("Content-Disposition") != `attachment; filename="krun-audit.csv"` ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected export response: %d %v", w.Code, w.Header())
	}
	body, hasBOM := strings.CutPrefix(w.Body.String(), "\uFEFF")
	if !hasBOM {
		t.Fatal("CSV is missing UTF-8 BOM")
	}
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAuditCSVExportAuthorizationAndQuery(t *testing.T) {
	s, _ := testServer(t)
	const path = "/api/v1/orgs/org-a/audit/export"
	other := auditlog.EncodeCursor(auditCursor{Scope: "organization:org-b", AsOf: now()})
	tests := []struct {
		query  string
		token  string
		status int
	}{
		{"", "", 401},
		{"?redact=false", "krun_cli_viewer", 403},
		{"?redact=no", "krun_cli_admin", 400},
		{"?redact=true&redact=false", "krun_cli_admin", 400},
		{"?redact=false&cursor=%ZZ", "krun_cli_admin", 400},
		{"?cursor=" + url.QueryEscape(other), "krun_cli_admin", 400},
	}
	for _, tt := range tests {
		w := request(s, "GET", path+tt.query, tt.token, "")
		if w.Code != tt.status || w.Header().Get("Content-Disposition") != "" {
			t.Errorf("%s returned %d, want %d with no attachment", tt.query, w.Code, tt.status)
		}
	}
	if rows := getAuditCSV(t, s, path); len(rows) != 1 {
		t.Fatalf("empty export should contain just the header, got %d rows", len(rows))
	}
}
