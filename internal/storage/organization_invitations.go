package storage

import (
	"context"
	"database/sql"
	"errors"
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
	return s.orm.NewSelect().Model((*models.OrgInvitationRedemption)(nil)).Where("invitation_id = ? AND user_id = ?", id, user).Exists(ctx)
}

func (s *Store) ListOrganizationInvitations(ctx context.Context, organization, after string, limit int) ([]domain.OrganizationInvitation, error) {
	cutoff := now().Add(-7 * 24 * time.Hour)

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
