package storage

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

const organizationInvitationLimit = 10

func invitationModel(in domain.OrganizationInvitation) models.OrgInvitationLink {
	return models.OrgInvitationLink{
		InvitationID: in.ID, OrgID: in.Organization, CreatorUserID: in.Creator,
		Role: in.Role, TargetUserID: in.TargetUser, TargetEmail: in.TargetEmail,
		TokenHash: in.TokenHash, MaxUses: in.MaxUses, UseCount: in.UseCount,
		CreatedAt: in.Created, ExpiresAt: in.Expires, RevokedAt: in.Revoked, Revision: in.Revision,
	}
}

func invitationDomain(row models.OrgInvitationLink) domain.OrganizationInvitation {
	return domain.OrganizationInvitation{
		ID: row.InvitationID, Organization: row.OrgID, Creator: row.CreatorUserID,
		Role: row.Role, TargetUser: row.TargetUserID, TargetEmail: row.TargetEmail,
		TokenHash: row.TokenHash, MaxUses: row.MaxUses, UseCount: row.UseCount,
		Created: row.CreatedAt, Expires: row.ExpiresAt, Revoked: row.RevokedAt, Revision: row.Revision,
	}.WithStatus(now())
}

func (s *Store) OrganizationInvitation(ctx context.Context, id, hash string) (domain.OrganizationInvitation, bool, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, in := range s.organizationInvitations {
			if id != "" && in.ID == id || hash != "" && in.TokenHash == hash {
				return in.WithStatus(now()), true, nil
			}
		}
		return domain.OrganizationInvitation{}, false, nil
	}
	var row models.OrgInvitationLink
	query := s.orm.NewSelect().Model(&row)
	if id != "" {
		query.Where("invitation_id = ?", id)
	} else {
		query.Where("token_hash = ?", hash)
	}
	err := query.Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OrganizationInvitation{}, false, nil
	}
	return invitationDomain(row), err == nil, err
}

func (s *Store) OrganizationInvitationRedeemed(ctx context.Context, id, user string) (bool, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.invitationRedemptions[id][user], nil
	}
	return s.orm.NewSelect().Model((*models.OrgInvitationRedemption)(nil)).Where("invitation_id = ? AND user_id = ?", id, user).Exists(ctx)
}

func (s *Store) ListOrganizationInvitations(ctx context.Context, organization, after string, limit int) ([]domain.OrganizationInvitation, error) {
	cutoff := now().Add(-7 * 24 * time.Hour)
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := []domain.OrganizationInvitation{}
		cursor, hasCursor := s.organizationInvitations[after]
		for _, in := range s.organizationInvitations {
			if in.Organization != organization || !in.Expires.After(cutoff) {
				continue
			}
			if after != "" && (!hasCursor || cursor.Organization != organization || in.Created.After(cursor.Created) || in.Created.Equal(cursor.Created) && in.ID >= after) {
				continue
			}
			out = append(out, in.WithStatus(now()))
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Created.Equal(out[j].Created) {
				return out[i].ID > out[j].ID
			}
			return out[i].Created.After(out[j].Created)
		})
		if len(out) > limit {
			out = out[:limit]
		}
		return out, nil
	}
	rows := []models.OrgInvitationLink{}
	query := s.orm.NewSelect().Model(&rows).Where("org_id = ? AND expires_at > ?", organization, cutoff).
		OrderExpr("created_at DESC, invitation_id DESC").Limit(int64(limit))
	if after != "" {
		query.Where("(created_at, invitation_id) < (SELECT created_at, invitation_id FROM krun_org_invitation_links WHERE invitation_id = ? AND org_id = ?)", after, organization)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]domain.OrganizationInvitation, 0, len(rows))
	for _, row := range rows {
		out = append(out, invitationDomain(row))
	}
	return out, nil
}

func invitationLimitError() error {
	return domain.Problem(409, "INVITATION_LIMIT_REACHED", "Revoke unused invitation links before creating another")
}

func invitationConflict() error {
	return domain.Problem(409, "REVISION_CONFLICT", "Invitation changed")
}

func (s *Store) CreateOrganizationInvitation(ctx context.Context, in domain.OrganizationInvitation, orgRevision int, audit domain.Audit) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		org, found := s.memory.Organizations[in.Organization]
		if !found || org.Revision != orgRevision {
			return invitationConflict()
		}
		active := 0
		for _, existing := range s.organizationInvitations {
			if existing.Organization == in.Organization && existing.WithStatus(now()).Status == "active" {
				active++
			}
		}
		if active >= organizationInvitationLimit {
			return invitationLimitError()
		}
		if s.organizationInvitations == nil {
			s.organizationInvitations = map[string]domain.OrganizationInvitation{}
		}
		s.organizationInvitations[in.ID] = in
		org.Revision++
		s.memory.Organizations[org.ID] = org
		s.recordInvitationMemoryAudit(audit)
		return nil
	}
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	org := models.Organization{OrgID: in.Organization}
	if err := ormCAS(ctx, "Organization changed", tx.orm.NewUpdate().Model(&org).Set("revision = revision + 1").WherePK().Where("revision = ?", orgRevision)); err != nil {
		return err
	}
	count, err := tx.orm.NewSelect().Model((*models.OrgInvitationLink)(nil)).Where("org_id = ? AND revoked_at IS NULL AND expires_at > ? AND use_count < max_uses", in.Organization, now()).Count(ctx)
	if err != nil {
		return err
	}
	if count >= organizationInvitationLimit {
		return invitationLimitError()
	}
	row := invitationModel(in)
	if _, err := tx.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx); err != nil {
		return err
	}
	if err := writeInvitationAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RevokeOrganizationInvitation(ctx context.Context, organization, id string, audit domain.Audit) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		in, found := s.organizationInvitations[id]
		if !found || in.Organization != organization {
			return domain.NotFound()
		}
		if in.Revoked == nil {
			at := now()
			in.Revoked = &at
			in.Revision++
			s.organizationInvitations[id] = in
			s.recordInvitationMemoryAudit(audit)
		}
		return nil
	}
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	row := models.OrgInvitationLink{InvitationID: id}
	result, err := tx.orm.NewUpdate().Model(&row).Set("revoked_at = ?", now()).Set("revision = revision + 1").WherePK().
		Where("org_id = ? AND revoked_at IS NULL", organization).Exec(ctx)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		found, err := tx.orm.NewSelect().Model(&row).WherePK().Where("org_id = ?", organization).Exists(ctx)
		if err != nil {
			return err
		}
		if !found {
			return domain.NotFound()
		}
		return nil
	}
	if err := writeInvitationAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeInvitationAudit(ctx context.Context, tx ormTransaction, audit domain.Audit) error {
	var revision int
	if err := tx.QueryRow(ctx, `SELECT nextval('krun_revision_seq')`).Scan(&revision); err != nil {
		return err
	}
	audit.Revision = revision
	row := models.Meta{Key: "revision", Value: Itoa(revision)}
	if _, err := tx.orm.NewInsert().Model(&row).On("CONFLICT (key) DO UPDATE").Set("value = GREATEST(krun_meta.value::bigint, EXCLUDED.value::bigint)::text").Returning("NULL").Exec(ctx); err != nil {
		return err
	}
	return insertAudits(ctx, tx, []domain.Audit{audit})
}

func (s *Store) recordInvitationMemoryAudit(audit domain.Audit) {
	s.memory.Revision++
	audit.Revision = s.memory.Revision
	s.memoryAudits = append(s.memoryAudits, audit)
	s.memoryAudits = PruneMemoryAudits(s.memoryAudits, now())
	if s.onPersist != nil {
		s.onPersist()
	}
}
