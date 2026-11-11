package storage

import (
	"context"
	models "github.com/krun-dev/krun/internal/storage/models"

	"reflect"
	"sort"

	"github.com/krun-dev/krun/internal/domain"
)

// persistIAM writes only the rows that differ between before and after.
// Per AGENTS.md section 9 this is a best-effort guarantee: a handful of
// statements grouped so the metadata cannot contradict itself, never a
// whole-document rewrite and never containing network calls.
func (s *Store) persistIAM(ctx context.Context, before, after *domain.State, audits []domain.Audit) error {

	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if login := after.PendingLogin; login != nil && login.BrowserHash != "" {
		tag, updateErr := tx.Exec(ctx, `UPDATE krun_browsers SET generation=generation+1
			WHERE browser_hash=$1 AND generation=$2 AND expires_at > now()`, login.BrowserHash, login.ExpectedGeneration)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			if login.EmailChallengeHash != "" {
				return domain.Problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code")
			}
			return domain.ForbiddenCode("AUTH_STATE_INVALID", "Login state changed")
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
	if after.PendingLogin != nil && after.PendingLogin.EmailChallengeHash != "" {
		tag, consumeErr := tx.Exec(ctx, `UPDATE krun_email_logins
			SET attempts=attempts+1, consumed=true
			WHERE challenge_hash=$1 AND consumed=false AND attempts < 5 AND expires_at > now()`, after.PendingLogin.EmailChallengeHash)
		if consumeErr != nil {
			return consumeErr
		}
		if tag.RowsAffected() != 1 {
			return domain.Problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code")
		}
	}
	if err = writeIAMDiff(ctx, tx, before, after); err != nil {
		return err
	}
	if login := after.PendingLogin; login != nil {
		if login.DeleteSessionHash != "" {
			row := models.Session{SessionHash: login.DeleteSessionHash}
			if _, err = tx.orm.NewDelete().Model(&row).WherePK().Exec(ctx); err != nil {
				return err
			}
		}
		row := sessionModel(login.Session.Hash, login.Session)
		if _, err = tx.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx); err != nil {
			return err
		}
	}

	if revisionChanged {
		// nextval is monotonic but transactions may commit out of order. Keep the
		// visible high-water mark from ever moving backwards.
		row := models.Meta{Key: "revision", Value: Itoa(after.Revision)}
		if _, err = tx.orm.NewInsert().Model(&row).On("CONFLICT (key) DO UPDATE").Set("value = GREATEST(krun_meta.value::bigint, EXCLUDED.value::bigint)::text").Returning("NULL").Exec(ctx); err != nil {
			return err
		}
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeIAMDiff(ctx context.Context, tx ormTransaction, before, after *domain.State) error {
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
				return domain.Conflict("REVISION_CONFLICT", "Bootstrap state changed")
			}
		}
	}
	return nil
}

func Itoa(n int) string {
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

// writeIdentitySources persists the three-key Sources map as two tables. A
// provider id may only ever bind one source; a namespace may only ever bind
// one source; a source may be shared by several providers.

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

// splitNulKey splits the composite keys the in-memory maps use ("a\x00b").
func splitNulKey(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

func sameAssignment(a, b domain.Assignment) bool {
	return a.ID == b.ID && a.SubjectKind == b.SubjectKind && a.SubjectID == b.SubjectID &&
		a.Role == b.Role && a.ScopeKind == b.ScopeKind && a.ScopeID == b.ScopeID &&
		a.Revision == b.Revision && reflect.DeepEqual(a.Conditions, b.Conditions)
}

func sameAPIKey(a, b domain.APIKey) bool {
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
