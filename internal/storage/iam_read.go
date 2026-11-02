package storage

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

// loadIAM fills the identity and authorization half of st in one round trip.
// Authorization reads most of these tables on every request, so this is a batch
// rather than a sequence of queries.
func (s *Store) loadIAM(ctx context.Context, st *domain.State) error {
	batch := &pgx.Batch{}
	queueStateRead(ctx, batch, `SELECT user_id, source, subject, name, email, status, auth_version FROM krun_users`)
	queueStateRead(ctx, batch, `SELECT org_id, name, revision FROM krun_organizations`)
	queueStateRead(ctx, batch, `SELECT org_id, source FROM krun_org_sources ORDER BY source`)
	queueStateRead(ctx, batch, `SELECT org_id, user_id FROM krun_members`)
	queueStateRead(ctx, batch, `SELECT group_id, scope_kind, scope_id, name, revision FROM krun_groups`)
	queueStateRead(ctx, batch, `SELECT group_id, user_id FROM krun_group_members`)
	queueStateRead(ctx, batch, `SELECT assignment_id, subject_kind, subject_id, role, scope_kind, scope_id, conditions, revision FROM krun_assignments`)
	queueStateRead(ctx, batch, `SELECT resource_id, kind, org_id, name, revision FROM krun_resources`)
	queueStateRead(ctx, batch, `SELECT key_id, owner_user_id, org_id, scope_kind, name, prefix, policy, expires_at, revoked, revision FROM krun_api_keys`)
	queueStateRead(ctx, batch, `SELECT source, subject, expires_at FROM krun_invitations`)
	queueStateRead(ctx, batch, `SELECT source, fingerprint FROM krun_identity_sources`)
	queueStateRead(ctx, batch, `SELECT provider_id, source, namespace FROM krun_identity_providers`)
	queueStateRead(ctx, batch, `SELECT key, value FROM krun_meta`)

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	if err := readUsers(results, st); err != nil {
		return err
	}
	if err := readOrganizations(results, st); err != nil {
		return err
	}
	if err := readOrgSources(results, st); err != nil {
		return err
	}
	if err := readMembers(results, st); err != nil {
		return err
	}
	if err := readGroups(results, st); err != nil {
		return err
	}
	if err := readGroupMembers(results, st); err != nil {
		return err
	}
	if err := readAssignments(results, st); err != nil {
		return err
	}
	if err := readResources(results, st); err != nil {
		return err
	}
	if err := readAPIKeys(results, st); err != nil {
		return err
	}
	if err := readInvitations(results, st); err != nil {
		return err
	}
	if err := readIdentitySources(results, st); err != nil {
		return err
	}
	if err := readIdentityProviders(results, st); err != nil {
		return err
	}
	return readMeta(results, st)
}

func readUsers(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u domain.User
		if err = rows.Scan(&u.ID, &u.Source, &u.Subject, &u.Name, &u.Email, &u.Status, &u.AuthVersion); err != nil {
			return err
		}
		st.Users[u.ID] = u
	}
	return rows.Err()
}

func readOrganizations(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var o domain.Organization
		if err = rows.Scan(&o.ID, &o.Name, &o.Revision); err != nil {
			return err
		}
		o.Sources = []string{}
		st.Organizations[o.ID] = o
	}
	return rows.Err()
}

func readOrgSources(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var org, source string
		if err = rows.Scan(&org, &source); err != nil {
			return err
		}
		o := st.Organizations[org]
		o.Sources = append(o.Sources, source)
		st.Organizations[org] = o
	}
	return rows.Err()
}

func readMembers(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var org, user string
		if err = rows.Scan(&org, &user); err != nil {
			return err
		}
		st.Members[domain.MemberKey(org, user)] = domain.Member{Organization: org, User: user}
	}
	return rows.Err()
}

func readGroups(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var g domain.Group
		if err = rows.Scan(&g.ID, &g.ScopeKind, &g.ScopeID, &g.Name, &g.Revision); err != nil {
			return err
		}
		g.Members = []string{}
		st.Groups[g.ID] = g
	}
	return rows.Err()
}

func readGroupMembers(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var gid, uid string
		if err = rows.Scan(&gid, &uid); err != nil {
			return err
		}
		g := st.Groups[gid]
		g.Members = append(g.Members, uid)
		st.Groups[gid] = g
	}
	return rows.Err()
}

func readAssignments(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a domain.Assignment
		var conditions []byte
		if err = rows.Scan(&a.ID, &a.SubjectKind, &a.SubjectID, &a.Role, &a.ScopeKind, &a.ScopeID, &conditions, &a.Revision); err != nil {
			return err
		}
		if len(conditions) > 0 {
			if err = json.Unmarshal(conditions, &a.Conditions); err != nil {
				return err
			}
		}
		st.Assignments[a.ID] = a
	}
	return rows.Err()
}

func readResources(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r domain.Resource
		if err = rows.Scan(&r.ID, &r.Kind, &r.Organization, &r.Name, &r.Revision); err != nil {
			return err
		}
		st.Resources[r.ID] = r
	}
	return rows.Err()
}

func readAPIKeys(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k domain.APIKey
		var policy []byte
		if err = rows.Scan(&k.ID, &k.Owner, &k.Organization, &k.ScopeKind, &k.Name, &k.Prefix, &policy, &k.Expires, &k.Revoked, &k.Revision); err != nil {
			return err
		}
		if len(policy) > 0 {
			if err = json.Unmarshal(policy, &k.Policy); err != nil {
				return err
			}
		}
		st.Keys[k.ID] = k
	}
	return rows.Err()
}

func readInvitations(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source, subject string
		var expires = now()
		if err = rows.Scan(&source, &subject, &expires); err != nil {
			return err
		}
		st.Invitations[source+"\x00"+subject] = expires
	}
	return rows.Err()
}

func readIdentitySources(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source, fingerprint string
		if err = rows.Scan(&source, &fingerprint); err != nil {
			return err
		}
		st.Sources[source] = fingerprint
	}
	return rows.Err()
}

func readIdentityProviders(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var providerID, source, namespace string
		if err = rows.Scan(&providerID, &source, &namespace); err != nil {
			return err
		}
		st.Sources["provider:"+providerID] = source
		st.Sources["namespace:"+namespace] = source
	}
	return rows.Err()
}

func readMeta(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return err
		}
		switch key {
		case "revision":
			st.Revision = parseIntDefault(value)
		case "bootstrapped":
			st.Bootstrapped = value == "1"
		}
	}
	return rows.Err()
}

func parseIntDefault(raw string) int {
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
