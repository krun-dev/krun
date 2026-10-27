package control

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
)

// persistIAM writes only the rows that differ between before and after.
// Per AGENTS.md section 9 this is a best-effort guarantee: a handful of
// statements grouped so the metadata cannot contradict itself, never a
// whole-document rewrite and never containing network calls.
func (s *Store) persistIAM(ctx context.Context, before, after *State) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = writeIAMDiff(ctx, tx, before, after); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeIAMDiff(ctx context.Context, tx pgx.Tx, before, after *State) error {
	if err := writeIdentitySources(ctx, tx, before, after); err != nil {
		return err
	}
	if err := writeUsers(ctx, tx, before.Users, after.Users); err != nil {
		return err
	}
	if err := writeOrganizations(ctx, tx, before.Organizations, after.Organizations); err != nil {
		return err
	}
	if err := writeMembers(ctx, tx, before.Members, after.Members); err != nil {
		return err
	}
	if err := writeGroups(ctx, tx, before.Groups, after.Groups); err != nil {
		return err
	}
	if err := writeAssignments(ctx, tx, before.Assignments, after.Assignments); err != nil {
		return err
	}
	if err := writeResources(ctx, tx, before.Resources, after.Resources); err != nil {
		return err
	}
	if err := writeAPIKeys(ctx, tx, before, after); err != nil {
		return err
	}
	if err := writeInvitations(ctx, tx, before.Invitations, after.Invitations); err != nil {
		return err
	}
	if after.Revision != before.Revision {
		if _, err := tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('revision',$1) ON CONFLICT (key) DO UPDATE SET value=excluded.value`, itoa(after.Revision)); err != nil {
			return err
		}
	}
	if after.Bootstrapped != before.Bootstrapped {
		value := "0"
		if after.Bootstrapped {
			value = "1"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('bootstrapped',$1) ON CONFLICT (key) DO UPDATE SET value=excluded.value`, value); err != nil {
			return err
		}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// writeIdentitySources persists the three-key Sources map as two tables. A
// provider id may only ever bind one source; a namespace may only ever bind
// one source; a source may be shared by several providers.
func writeIdentitySources(ctx context.Context, tx pgx.Tx, before, after *State) error {
	for source, fingerprint := range after.Sources {
		if isProviderKey(source) {
			continue
		}
		if old, ok := before.Sources[source]; ok && old == fingerprint && !isProviderKey(source) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_identity_sources (source, fingerprint) VALUES ($1,$2) ON CONFLICT (source) DO UPDATE SET fingerprint=excluded.fingerprint`, source, fingerprint); err != nil {
			return err
		}
	}
	for key, value := range after.Sources {
		providerID, ok := providerIDFromKey(key)
		if !ok {
			continue
		}
		old, existed := before.Sources[key]
		if existed && old == value {
			continue
		}
		namespace := ""
		// The provider→namespace binding is derived from the flat keys the
		// registration path writes ("provider:<id>" and "namespace:<ns>").
		for candidate, source := range after.Sources {
			if ns, ok := namespaceFromKey(candidate); ok && source == value {
				namespace = ns
				break
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_identity_providers (provider_id, source, namespace) VALUES ($1,$2,$3) ON CONFLICT (provider_id) DO UPDATE SET source=excluded.source, namespace=excluded.namespace`, providerID, value, namespace); err != nil {
			return err
		}
	}
	for key := range before.Sources {
		if _, ok := after.Sources[key]; ok {
			continue
		}
		if source, ok := providerIDFromKey(key); ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_identity_providers WHERE provider_id=$1`, source); err != nil {
				return err
			}
			continue
		}
		if isProviderKey(key) {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM krun_identity_sources WHERE source=$1`, key); err != nil {
			return err
		}
	}
	return nil
}

func isProviderKey(key string) bool {
	if _, ok := providerIDFromKey(key); ok {
		return true
	}
	_, ok := namespaceFromKey(key)
	return ok
}

func providerIDFromKey(key string) (string, bool) {
	const prefix = "provider:"
	if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
		return "", false
	}
	return key[len(prefix):], true
}

func namespaceFromKey(key string) (string, bool) {
	const prefix = "namespace:"
	if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
		return "", false
	}
	return key[len(prefix):], true
}

func writeUsers(ctx context.Context, tx pgx.Tx, before, after map[string]User) error {
	for id, u := range after {
		if old, ok := before[id]; ok && old == u {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_users (user_id, source, subject, name, email, status, auth_version) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (user_id) DO UPDATE SET source=excluded.source, subject=excluded.subject, name=excluded.name, email=excluded.email, status=excluded.status, auth_version=excluded.auth_version, updated_at=now()`, id, u.Source, u.Subject, u.Name, u.Email, u.Status, u.AuthVersion); err != nil {
			return err
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_users WHERE user_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeOrganizations(ctx context.Context, tx pgx.Tx, before, after map[string]Organization) error {
	for id, o := range after {
		old, existed := before[id]
		if existed && old.Name == o.Name && sameStrings(old.Sources, o.Sources) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_organizations (org_id, name) VALUES ($1,$2) ON CONFLICT (org_id) DO UPDATE SET name=excluded.name`, id, o.Name); err != nil {
			return err
		}
		if !existed || !sameStrings(old.Sources, o.Sources) {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_org_sources WHERE org_id=$1`, id); err != nil {
				return err
			}
			for _, source := range o.Sources {
				if _, err := tx.Exec(ctx, `INSERT INTO krun_org_sources (org_id, source) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, source); err != nil {
					return err
				}
			}
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_org_sources WHERE org_id=$1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM krun_organizations WHERE org_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeMembers(ctx context.Context, tx pgx.Tx, before, after map[string]Member) error {
	for key, m := range after {
		if _, ok := before[key]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_members (org_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, m.Organization, m.User); err != nil {
			return err
		}
	}
	for key, m := range before {
		if _, ok := after[key]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM krun_members WHERE org_id=$1 AND user_id=$2`, m.Organization, m.User); err != nil {
			return err
		}
	}
	return nil
}

func writeGroups(ctx context.Context, tx pgx.Tx, before, after map[string]Group) error {
	for id, g := range after {
		old, existed := before[id]
		if existed && old.Name == g.Name && old.Revision == g.Revision && sameStrings(old.Members, g.Members) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_groups (group_id, scope_kind, scope_id, name, revision) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (group_id) DO UPDATE SET scope_kind=excluded.scope_kind, scope_id=excluded.scope_id, name=excluded.name, revision=excluded.revision`, id, g.ScopeKind, g.ScopeID, g.Name, g.Revision); err != nil {
			return err
		}
		if !existed || !sameStrings(old.Members, g.Members) {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_group_members WHERE group_id=$1`, id); err != nil {
				return err
			}
			for _, user := range g.Members {
				if _, err := tx.Exec(ctx, `INSERT INTO krun_group_members (group_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, user); err != nil {
					return err
				}
			}
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_group_members WHERE group_id=$1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM krun_groups WHERE group_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeAssignments(ctx context.Context, tx pgx.Tx, before, after map[string]Assignment) error {
	for id, a := range after {
		if old, ok := before[id]; ok && sameAssignment(old, a) {
			continue
		}
		conditions, err := json.Marshal(a.Conditions)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_assignments (assignment_id, subject_kind, subject_id, role, scope_kind, scope_id, conditions, revision) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (assignment_id) DO UPDATE SET subject_kind=excluded.subject_kind, subject_id=excluded.subject_id, role=excluded.role, scope_kind=excluded.scope_kind, scope_id=excluded.scope_id, conditions=excluded.conditions, revision=excluded.revision`, id, a.SubjectKind, a.SubjectID, a.Role, a.ScopeKind, a.ScopeID, conditions, a.Revision); err != nil {
			return err
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_assignments WHERE assignment_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeResources(ctx context.Context, tx pgx.Tx, before, after map[string]Resource) error {
	for id, r := range after {
		if old, ok := before[id]; ok && old == r {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_resources (resource_id, kind, org_id, name, revision) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (resource_id) DO UPDATE SET kind=excluded.kind, org_id=excluded.org_id, name=excluded.name, revision=excluded.revision`, id, r.Kind, r.Organization, r.Name, r.Revision); err != nil {
			return err
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_resources WHERE resource_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeAPIKeys keeps the authentication hash in its own table so metadata
// queries never read it.
func writeAPIKeys(ctx context.Context, tx pgx.Tx, before, after *State) error {
	for id, k := range after.Keys {
		if old, ok := before.Keys[id]; ok && sameAPIKey(old, k) {
			continue
		}
		policy, err := json.Marshal(k.Policy)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_api_keys (key_id, owner_user_id, org_id, scope_kind, name, prefix, policy, expires_at, revoked, revision) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (key_id) DO UPDATE SET owner_user_id=excluded.owner_user_id, org_id=excluded.org_id, scope_kind=excluded.scope_kind, name=excluded.name, prefix=excluded.prefix, policy=excluded.policy, expires_at=excluded.expires_at, revoked=excluded.revoked, revision=excluded.revision`, id, k.Owner, k.Organization, k.ScopeKind, k.Name, k.Prefix, policy, k.Expires, k.Revoked, k.Revision); err != nil {
			return err
		}
	}
	for id := range before.Keys {
		if _, ok := after.Keys[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_api_keys WHERE key_id=$1`, id); err != nil {
				return err
			}
		}
	}
	for id, hash := range after.KeyHashes {
		if old, ok := before.KeyHashes[id]; ok && old == hash {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_api_key_hashes (key_id, hash) VALUES ($1,$2) ON CONFLICT (key_id) DO UPDATE SET hash=excluded.hash`, id, hash); err != nil {
			return err
		}
	}
	for id := range before.KeyHashes {
		if _, ok := after.KeyHashes[id]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM krun_api_key_hashes WHERE key_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeInvitations(ctx context.Context, tx pgx.Tx, before, after map[string]time.Time) error {
	for key, expires := range after {
		old, ok := before[key]
		if ok && old.Equal(expires) {
			continue
		}
		source, subject := splitNulKey(key)
		if _, err := tx.Exec(ctx, `INSERT INTO krun_invitations (source, subject, expires_at) VALUES ($1,$2,$3) ON CONFLICT (source, subject) DO UPDATE SET expires_at=excluded.expires_at`, source, subject, expires); err != nil {
			return err
		}
	}
	for key := range before {
		if _, ok := after[key]; ok {
			continue
		}
		source, subject := splitNulKey(key)
		if _, err := tx.Exec(ctx, `DELETE FROM krun_invitations WHERE source=$1 AND subject=$2`, source, subject); err != nil {
			return err
		}
	}
	return nil
}

// splitNulKey splits the composite keys the in-memory maps use ("a\x00b").
func splitNulKey(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

func sameAssignment(a, b Assignment) bool {
	return a.ID == b.ID && a.SubjectKind == b.SubjectKind && a.SubjectID == b.SubjectID &&
		a.Role == b.Role && a.ScopeKind == b.ScopeKind && a.ScopeID == b.ScopeID &&
		a.Revision == b.Revision && reflect.DeepEqual(a.Conditions, b.Conditions)
}

func sameAPIKey(a, b APIKey) bool {
	if (a.Expires == nil) != (b.Expires == nil) {
		return false
	}
	if a.Expires != nil && !a.Expires.Equal(*b.Expires) {
		return false
	}
	return a.ID == b.ID && a.Owner == b.Owner && a.Organization == b.Organization &&
		a.ScopeKind == b.ScopeKind && a.Name == b.Name && a.Prefix == b.Prefix &&
		a.Revoked == b.Revoked && a.Revision == b.Revision && reflect.DeepEqual(a.Policy, b.Policy)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
