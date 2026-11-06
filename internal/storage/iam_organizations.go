package storage

import (
	"context"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func writeOrganizations(ctx context.Context, tx ormTransaction, before, after map[string]domain.Organization) error {
	for id, organization := range after {
		previous, existed := before[id]
		if existed && previous.Name == organization.Name && previous.Revision == organization.Revision && sameStrings(previous.Sources, organization.Sources) {
			continue
		}
		row := models.Organization{OrgID: id, Name: organization.Name, Revision: organization.Revision}
		if !existed {
			if err := ormCAS(ctx, "Organization changed", tx.orm.NewInsert().Model(&row).Column("org_id", "name", "revision").On("CONFLICT (org_id) DO NOTHING").Returning("NULL")); err != nil {
				return err
			}
		} else if err := ormCAS(ctx, "Organization changed", tx.orm.NewUpdate().Model(&row).Column("name", "revision").WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
		if !existed || !sameStrings(previous.Sources, organization.Sources) {
			if _, err := tx.orm.NewDelete().Model((*models.OrgSource)(nil)).Where("org_id = ?", id).Exec(ctx); err != nil {
				return err
			}
			for _, source := range organization.Sources {
				row := models.OrgSource{OrgID: id, Source: source}
				if _, err := tx.orm.NewInsert().Model(&row).On("CONFLICT DO NOTHING").Returning("NULL").Exec(ctx); err != nil {
					return err
				}
			}
		}
	}
	for id, previous := range before {
		if _, exists := after[id]; exists {
			continue
		}
		row := models.Organization{OrgID: id}
		if _, err := tx.Exec(ctx, `DELETE FROM krun_org_invitation_redemptions WHERE invitation_id IN (SELECT invitation_id FROM krun_org_invitation_links WHERE org_id=$1)`, id); err != nil {
			return err
		}
		if _, err := tx.orm.NewDelete().Model((*models.OrgInvitationLink)(nil)).Where("org_id = ?", id).Exec(ctx); err != nil {
			return err
		}
		if err := ormCAS(ctx, "Organization changed", tx.orm.NewDelete().Model(&row).WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
		if _, err := tx.orm.NewDelete().Model((*models.OrgSource)(nil)).Where("org_id = ?", id).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
func writeMembers(ctx context.Context, tx ormTransaction, before, after map[string]domain.Member) error {
	for key, member := range after {
		if _, exists := before[key]; exists {
			continue
		}
		row := models.Member{OrgID: member.Organization, UserID: member.User}
		if _, err := tx.orm.NewInsert().Model(&row).Column("org_id", "user_id").On("CONFLICT DO NOTHING").Returning("NULL").Exec(ctx); err != nil {
			return err
		}
	}
	for key, member := range before {
		if _, exists := after[key]; exists {
			continue
		}
		row := models.Member{OrgID: member.Organization, UserID: member.User}
		if _, err := tx.orm.NewDelete().Model(&row).WherePK().Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
