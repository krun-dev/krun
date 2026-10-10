package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/krun-dev/krun/internal/configfile"
	"github.com/krun-dev/krun/internal/localize"
)

const auditRetention = 7 * 24 * time.Hour

func pruneAudits(st *State, at time.Time) {
	cutoff := at.Add(-auditRetention)
	kept := st.Audits[:0]
	for _, entry := range st.Audits {
		if !entry.Time.Before(cutoff) {
			kept = append(kept, entry)
		}
	}
	clear(st.Audits[len(kept):])
	st.Audits = kept
}

// Update also prunes on every committed transaction. This loop covers idle time.
func (s *Store) retainAudits(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Update(ctx, func(*State) error { return nil }); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, localize.Text(localize.Language(ctx), "krun audit retention cleanup failed:"), localize.Error(localize.Language(ctx), err))
			}
		}
	}
}

type auditCursor struct {
	Scope  string     `json:"scope"`
	AsOf   time.Time  `json:"as_of"`
	Before *time.Time `json:"before,omitempty"`
	ID     string     `json:"id,omitempty"`
}

type auditPage struct {
	Items         []Audit   `json:"items"`
	Total         int       `json:"total"`
	Limit         int       `json:"limit"`
	HasMore       bool      `json:"has_more"`
	Cursor        string    `json:"cursor"`
	NextCursor    string    `json:"next_cursor"`
	AsOf          time.Time `json:"as_of"`
	RetentionDays int       `json:"retention_days"`
}

func (s *Server) encodeAuditCursor(cursor auditCursor) string {
	// A cursor contains pagination parameters only; auditList validates scope,
	// times and current permissions on every request.
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (s *Server) auditList(st *State, p Principal, r *http.Request) (any, int, error) {
	at := now()
	t := systemTarget()
	if r.PathValue("org") != "" {
		var e error
		t, e = s.orgTarget(st, r)
		if e != nil {
			return nil, 0, e
		}
	}
	if e := s.require(st, p, r, "audit.read", t); e != nil {
		return nil, 0, e
	}
	query := r.URL.Query()
	limit := 50
	if values, ok := query["limit"]; ok {
		var err error
		if len(values) != 1 {
			return nil, 0, problem(400, "INVALID_REQUEST", "Use one limit")
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 500 {
			return nil, 0, problem(400, "INVALID_REQUEST", "Audit limit must be between 1 and 500")
		}
	}
	if len(query["cursor"]) > 1 {
		return nil, 0, problem(400, "INVALID_REQUEST", "Use one cursor")
	}
	cursor := auditCursor{Scope: t.Kind + ":" + t.ID, AsOf: at}
	current := query.Get("cursor")
	if current != "" {
		if len(current) > 2048 {
			return nil, 0, problem(400, "INVALID_REQUEST", "Invalid audit cursor")
		}
		raw, err := base64.RawURLEncoding.DecodeString(current)
		if err != nil || configfile.DecodeJSON(raw, &cursor) != nil || cursor.Scope != t.Kind+":"+t.ID || cursor.AsOf.IsZero() || cursor.AsOf.After(at) || (cursor.Before == nil) != (cursor.ID == "") || (cursor.Before != nil && cursor.Before.After(cursor.AsOf)) {
			return nil, 0, problem(400, "INVALID_REQUEST", "Invalid audit cursor")
		}
		if cursor.AsOf.Before(at.Add(-auditRetention)) {
			return nil, 0, problem(409, "AUDIT_CURSOR_EXPIRED", "Audit pagination expired. Refresh the list.")
		}
	} else {
		current = s.encodeAuditCursor(cursor)
	}
	matching := []Audit{}
	cutoff := at.Add(-auditRetention)
	for _, entry := range st.Audits {
		if entry.Time.Before(cutoff) || entry.Time.After(cursor.AsOf) {
			continue
		}
		belongs := t.Kind == "system" || entry.Organization == t.ID
		if !belongs {
			resource, ok := st.Resources[entry.Resource]
			belongs = ok && resource.Organization == t.ID
		}
		if belongs {
			matching = append(matching, entry)
		}
	}
	sort.Slice(matching, func(i, j int) bool {
		if matching[i].Time.Equal(matching[j].Time) {
			return matching[i].ID > matching[j].ID
		}
		return matching[i].Time.After(matching[j].Time)
	})
	page := auditPage{Items: []Audit{}, Total: len(matching), Limit: limit, Cursor: current, AsOf: cursor.AsOf, RetentionDays: 7}
	for _, entry := range matching {
		if cursor.Before != nil && (entry.Time.After(*cursor.Before) || (entry.Time.Equal(*cursor.Before) && entry.ID >= cursor.ID)) {
			continue
		}
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, entry)
	}
	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		cursor.Before, cursor.ID = &last.Time, last.ID
		page.NextCursor = s.encodeAuditCursor(cursor)
	}
	return page, 200, nil
}
