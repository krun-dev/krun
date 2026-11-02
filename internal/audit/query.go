package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/configfile"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage"
)

type Cursor struct {
	Scope  string     `json:"scope"`
	AsOf   time.Time  `json:"as_of"`
	Before *time.Time `json:"before,omitempty"`
	ID     string     `json:"id,omitempty"`
}

type Page struct {
	Items         []domain.Audit `json:"items"`
	Total         int            `json:"total"`
	Limit         int            `json:"limit"`
	HasMore       bool           `json:"has_more"`
	Cursor        string         `json:"cursor"`
	NextCursor    string         `json:"next_cursor"`
	AsOf          time.Time      `json:"as_of"`
	RetentionDays int            `json:"retention_days"`
}

func EncodeCursor(cursor Cursor) string {
	// A cursor contains pagination parameters only; auditList validates scope,
	// times and current permissions on every request.
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

type Reader interface {
	ReadAudits(context.Context, time.Time, string, int) ([]domain.Audit, error)
}

func List(ctx context.Context, store Reader, st *domain.State, t authorization.Target, limit int, current string, at time.Time) (Page, error) {
	cursor := Cursor{Scope: t.Kind + ":" + t.ID, AsOf: at}
	if current != "" {
		if len(current) > 2048 {
			return Page{}, domain.Problem(400, "INVALID_REQUEST", "Invalid audit cursor")
		}
		raw, err := base64.RawURLEncoding.DecodeString(current)
		if err != nil || configfile.DecodeJSON(raw, &cursor) != nil || cursor.Scope != t.Kind+":"+t.ID || cursor.AsOf.IsZero() || cursor.AsOf.After(at) || (cursor.Before == nil) != (cursor.ID == "") || (cursor.Before != nil && cursor.Before.After(cursor.AsOf)) {
			return Page{}, domain.Problem(400, "INVALID_REQUEST", "Invalid audit cursor")
		}
		if cursor.AsOf.Before(at.Add(-storage.AuditRetention)) {
			return Page{}, domain.Problem(409, "AUDIT_CURSOR_EXPIRED", "Audit pagination expired. Refresh the list.")
		}
	} else {
		current = EncodeCursor(cursor)
	}
	scope := ""
	if t.Kind != "system" {
		scope = t.ID
	}
	cutoff := at.Add(-storage.AuditRetention)
	entries, err := store.ReadAudits(ctx, cutoff, scope, 5000)
	if err != nil {
		return Page{}, domain.Problem(503, "AUTHORIZATION_UNAVAILABLE", "Control service unavailable")
	}
	matching := []domain.Audit{}
	for _, entry := range entries {
		if entry.Time.After(cursor.AsOf) {
			continue
		}
		if t.Kind == "system" || entry.Organization == t.ID {
			matching = append(matching, entry)
			continue
		}
		resource, ok := st.Resources[entry.Resource]
		if ok && resource.Organization == t.ID {
			matching = append(matching, entry)
		}
	}
	page := Page{Items: []domain.Audit{}, Total: len(matching), Limit: limit, Cursor: current, AsOf: cursor.AsOf, RetentionDays: 7}
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
		page.NextCursor = EncodeCursor(cursor)
	}
	return page, nil

}
