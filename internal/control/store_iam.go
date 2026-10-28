package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// persistIAM writes only the rows that differ between before and after.
// Per AGENTS.md section 9 this is a best-effort guarantee: a handful of
// statements grouped so the metadata cannot contradict itself, never a
// whole-document rewrite and never containing network calls.
func (s *Store) persistIAM(ctx context.Context, before, after *State, audits []Audit) error {
	topologyChanged := workerIAMTopologyChanged(before, after)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if login := after.pendingLogin; login != nil && login.BrowserHash != "" {
		tag, updateErr := tx.Exec(ctx, `UPDATE krun_browsers SET generation=generation+1
			WHERE browser_hash=$1 AND generation=$2 AND expires_at > now()`, login.BrowserHash, login.ExpectedGeneration)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			if login.EmailChallengeHash != "" {
				return problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code")
			}
			return problem(403, "AUTH_STATE_INVALID", "Login state changed")
		}
	}
	revisionChanged := after.Revision != before.Revision
	if revisionChanged {
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT nextval('krun_revision_seq')`).Scan(&revision); err != nil {
			return err
		}
		after.Revision = int(revision)
		for i := range audits {
			audits[i].Revision = after.Revision
		}
	}
	if after.pendingLogin != nil && after.pendingLogin.EmailChallengeHash != "" {
		tag, consumeErr := tx.Exec(ctx, `UPDATE krun_email_logins
			SET attempts=attempts+1, consumed=true
			WHERE challenge_hash=$1 AND consumed=false AND attempts < 5 AND expires_at > now()`, after.pendingLogin.EmailChallengeHash)
		if consumeErr != nil {
			return consumeErr
		}
		if tag.RowsAffected() != 1 {
			return problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code")
		}
	}
	if err = writeIAMDiff(ctx, tx, before, after); err != nil {
		return err
	}
	if login := after.pendingLogin; login != nil {
		if login.DeleteSessionHash != "" {
			if _, err = tx.Exec(ctx, `DELETE FROM krun_sessions WHERE session_hash=$1`, login.DeleteSessionHash); err != nil {
				return err
			}
		}
		session := login.Session
		if _, err = tx.Exec(ctx, `INSERT INTO krun_sessions
			(session_hash, user_id, csrf, auth_version, cli, created_at, seen_at, absolute_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, session.Hash, session.User, session.CSRF,
			session.AuthVersion, session.CLI, session.Created, session.Seen, session.Absolute); err != nil {
			return err
		}
	}
	if revisionChanged {
		// nextval is monotonic but transactions may commit out of order. Keep the
		// visible high-water mark from ever moving backwards.
		if _, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('revision',$1)
			ON CONFLICT (key) DO UPDATE SET value=GREATEST(krun_meta.value::bigint, excluded.value::bigint)::text`, itoa(after.Revision)); err != nil {
			return err
		}
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	if topologyChanged {
		if err = bumpWorkerTopologyGeneration(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func workerIAMTopologyChanged(before, after *State) bool {
	if len(before.Organizations) != len(after.Organizations) {
		return true
	}
	for id := range before.Organizations {
		if _, ok := after.Organizations[id]; !ok {
			return true
		}
	}
	serviceResources := func(resources map[string]Resource) map[string]Resource {
		out := map[string]Resource{}
		for id, resource := range resources {
			if resource.Kind == "service" {
				out[id] = resource
			}
		}
		return out
	}
	oldServices, newServices := serviceResources(before.Resources), serviceResources(after.Resources)
	if len(oldServices) != len(newServices) {
		return true
	}
	for id, old := range oldServices {
		current, ok := newServices[id]
		if !ok || current.Organization != old.Organization {
			return true
		}
	}
	return false
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
	if after.Bootstrapped != before.Bootstrapped {
		value := "0"
		if after.Bootstrapped {
			value = "1"
		}
		tag, err := tx.Exec(ctx, `UPDATE krun_meta SET value=$1 WHERE key='bootstrapped' AND value=$2`, value, boolValue(before.Bootstrapped))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			tag, err = tx.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('bootstrapped',$1) ON CONFLICT (key) DO NOTHING`, value)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return problem(409, "REVISION_CONFLICT", "Bootstrap state changed")
			}
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

func boolValue(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func execRevisionCAS(ctx context.Context, tx pgx.Tx, message, query string, args ...any) error {
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return problem(409, "REVISION_CONFLICT", message)
		}
		return err
	}
	if tag.RowsAffected() != 1 {
		return problem(409, "REVISION_CONFLICT", message)
	}
	return nil
}

// writeIdentitySources persists the three-key Sources map as two tables. A
// provider id may only ever bind one source; a namespace may only ever bind
// one source; a source may be shared by several providers.
func writeIdentitySources(ctx context.Context, tx pgx.Tx, before, after *State) error {
	for _, source := range sortedKeys(after.Sources) {
		fingerprint := after.Sources[source]
		if isProviderKey(source) {
			continue
		}
		if old, ok := before.Sources[source]; ok && old == fingerprint && !isProviderKey(source) {
			continue
		}
		tag, err := tx.Exec(ctx, `INSERT INTO krun_identity_sources (source, fingerprint) VALUES ($1,$2) ON CONFLICT (source) DO NOTHING`, source, fingerprint)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var actual string
			if err = tx.QueryRow(ctx, `SELECT fingerprint FROM krun_identity_sources WHERE source=$1`, source).Scan(&actual); err != nil {
				return err
			}
			if actual != fingerprint {
				return problem(409, "REVISION_CONFLICT", "Identity source cannot be reassigned")
			}
		}
	}
	for _, key := range sortedKeys(after.Sources) {
		value := after.Sources[key]
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
		for _, candidate := range sortedKeys(after.Sources) {
			source := after.Sources[candidate]
			if ns, ok := namespaceFromKey(candidate); ok && source == value {
				namespace = ns
				break
			}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO krun_identity_providers (provider_id, source, namespace) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, providerID, value, namespace)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var actualSource, actualNamespace string
			if err = tx.QueryRow(ctx, `SELECT source, namespace FROM krun_identity_providers WHERE provider_id=$1`, providerID).Scan(&actualSource, &actualNamespace); err != nil {
				if err == pgx.ErrNoRows {
					return problem(409, "REVISION_CONFLICT", "Identity namespace already registered")
				}
				return err
			}
			if actualSource != value || actualNamespace != namespace {
				return problem(409, "REVISION_CONFLICT", "Identity provider cannot be reassigned")
			}
		}
	}
	for _, key := range sortedKeys(before.Sources) {
		if _, ok := after.Sources[key]; ok {
			continue
		}
		return problem(409, "REVISION_CONFLICT", "Identity source registrations are permanent")
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
		old, existed := before[id]
		if existed && old == u {
			continue
		}
		if !existed {
			if err := execRevisionCAS(ctx, tx, "Identity was created concurrently", `INSERT INTO krun_users (user_id, source, subject, name, email, status, auth_version) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, id, u.Source, u.Subject, u.Name, u.Email, u.Status, u.AuthVersion); err != nil {
				return err
			}
			continue
		}
		if err := execRevisionCAS(ctx, tx, "User changed", `UPDATE krun_users SET source=$2, subject=$3, name=$4, email=$5, status=$6, auth_version=$7, updated_at=now()
			WHERE user_id=$1 AND auth_version=$8 AND source=$9 AND subject=$10 AND name=$11 AND email=$12 AND status=$13`, id, u.Source, u.Subject, u.Name, u.Email, u.Status, u.AuthVersion, old.AuthVersion, old.Source, old.Subject, old.Name, old.Email, old.Status); err != nil {
			return err
		}
	}
	for id, old := range before {
		if _, ok := after[id]; !ok {
			if err := execRevisionCAS(ctx, tx, "User changed", `DELETE FROM krun_users WHERE user_id=$1 AND auth_version=$2 AND source=$3 AND subject=$4 AND name=$5 AND email=$6 AND status=$7`, id, old.AuthVersion, old.Source, old.Subject, old.Name, old.Email, old.Status); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeOrganizations(ctx context.Context, tx pgx.Tx, before, after map[string]Organization) error {
	for id, o := range after {
		old, existed := before[id]
		if existed && old.Name == o.Name && old.Revision == o.Revision && sameStrings(old.Sources, o.Sources) {
			continue
		}
		if !existed {
			if err := execRevisionCAS(ctx, tx, "Organization changed", `INSERT INTO krun_organizations (org_id, name, revision) VALUES ($1,$2,$3) ON CONFLICT (org_id) DO NOTHING`, id, o.Name, o.Revision); err != nil {
				return err
			}
		} else if err := execRevisionCAS(ctx, tx, "Organization changed", `UPDATE krun_organizations SET name=$2, revision=$3 WHERE org_id=$1 AND revision=$4`, id, o.Name, o.Revision, old.Revision); err != nil {
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
	for id, old := range before {
		if _, ok := after[id]; !ok {
			if err := execRevisionCAS(ctx, tx, "Organization changed", `DELETE FROM krun_organizations WHERE org_id=$1 AND revision=$2`, id, old.Revision); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM krun_org_sources WHERE org_id=$1`, id); err != nil {
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
		if existed && old.ScopeKind == g.ScopeKind && old.ScopeID == g.ScopeID && old.Name == g.Name && old.Revision == g.Revision && sameStrings(old.Members, g.Members) {
			continue
		}
		if !existed {
			if err := execRevisionCAS(ctx, tx, "Group changed", `INSERT INTO krun_groups (group_id, scope_kind, scope_id, name, revision) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (group_id) DO NOTHING`, id, g.ScopeKind, g.ScopeID, g.Name, g.Revision); err != nil {
				return err
			}
		} else if err := execRevisionCAS(ctx, tx, "Group changed", `UPDATE krun_groups SET scope_kind=$2, scope_id=$3, name=$4, revision=$5 WHERE group_id=$1 AND revision=$6`, id, g.ScopeKind, g.ScopeID, g.Name, g.Revision, old.Revision); err != nil {
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
	for id, old := range before {
		if _, ok := after[id]; !ok {
			if err := execRevisionCAS(ctx, tx, "Group changed", `DELETE FROM krun_groups WHERE group_id=$1 AND revision=$2`, id, old.Revision); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM krun_group_members WHERE group_id=$1`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeAssignments(ctx context.Context, tx pgx.Tx, before, after map[string]Assignment) error {
	for id, a := range after {
		old, existed := before[id]
		if existed && sameAssignment(old, a) {
			continue
		}
		conditions, err := json.Marshal(a.Conditions)
		if err != nil {
			return err
		}
		if !existed {
			if err = execRevisionCAS(ctx, tx, "Assignment changed", `INSERT INTO krun_assignments (assignment_id, subject_kind, subject_id, role, scope_kind, scope_id, conditions, revision) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (assignment_id) DO NOTHING`, id, a.SubjectKind, a.SubjectID, a.Role, a.ScopeKind, a.ScopeID, conditions, a.Revision); err != nil {
				return err
			}
		} else if err = execRevisionCAS(ctx, tx, "Assignment changed", `UPDATE krun_assignments SET subject_kind=$2, subject_id=$3, role=$4, scope_kind=$5, scope_id=$6, conditions=$7, revision=$8 WHERE assignment_id=$1 AND revision=$9`, id, a.SubjectKind, a.SubjectID, a.Role, a.ScopeKind, a.ScopeID, conditions, a.Revision, old.Revision); err != nil {
			return err
		}
	}
	for id, old := range before {
		if _, ok := after[id]; !ok {
			if err := execRevisionCAS(ctx, tx, "Assignment changed", `DELETE FROM krun_assignments WHERE assignment_id=$1 AND revision=$2`, id, old.Revision); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeResources(ctx context.Context, tx pgx.Tx, before, after map[string]Resource) error {
	for id, r := range after {
		old, existed := before[id]
		if existed && old == r {
			continue
		}
		if !existed {
			if err := execRevisionCAS(ctx, tx, "Resource changed", `INSERT INTO krun_resources (resource_id, kind, org_id, name, revision) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (resource_id) DO NOTHING`, id, r.Kind, r.Organization, r.Name, r.Revision); err != nil {
				return err
			}
		} else if err := execRevisionCAS(ctx, tx, "Resource changed", `UPDATE krun_resources SET kind=$2, org_id=$3, name=$4, revision=$5 WHERE resource_id=$1 AND revision=$6`, id, r.Kind, r.Organization, r.Name, r.Revision, old.Revision); err != nil {
			return err
		}
	}
	for id, old := range before {
		if _, ok := after[id]; !ok {
			if err := execRevisionCAS(ctx, tx, "Resource changed", `DELETE FROM krun_resources WHERE resource_id=$1 AND revision=$2`, id, old.Revision); err != nil {
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
		old, existed := before.Keys[id]
		if existed && sameAPIKey(old, k) {
			continue
		}
		policy, err := json.Marshal(k.Policy)
		if err != nil {
			return err
		}
		if !existed {
			if err = execRevisionCAS(ctx, tx, "Key changed", `INSERT INTO krun_api_keys (key_id, owner_user_id, org_id, scope_kind, name, prefix, policy, expires_at, revoked, revision) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (key_id) DO NOTHING`, id, k.Owner, k.Organization, k.ScopeKind, k.Name, k.Prefix, policy, k.Expires, k.Revoked, k.Revision); err != nil {
				return err
			}
		} else if err = execRevisionCAS(ctx, tx, "Key changed", `UPDATE krun_api_keys SET owner_user_id=$2, org_id=$3, scope_kind=$4, name=$5, prefix=$6, policy=$7, expires_at=$8, revoked=$9, revision=$10 WHERE key_id=$1 AND revision=$11`, id, k.Owner, k.Organization, k.ScopeKind, k.Name, k.Prefix, policy, k.Expires, k.Revoked, k.Revision, old.Revision); err != nil {
			return err
		}
	}
	for id, old := range before.Keys {
		if _, ok := after.Keys[id]; !ok {
			if err := execRevisionCAS(ctx, tx, "Key changed", `DELETE FROM krun_api_keys WHERE key_id=$1 AND revision=$2`, id, old.Revision); err != nil {
				return err
			}
			// Hashes are deliberately absent from generic IAM snapshots. Delete
			// the private row from the metadata deletion signal instead of relying
			// on a KeyHashes before-image that the request never loaded.
			if _, err := tx.Exec(ctx, `DELETE FROM krun_api_key_hashes WHERE key_id=$1`, id); err != nil {
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
