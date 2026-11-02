package storage

import (
	"context"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func writeGroups(ctx context.Context, tx ormTransaction, before, after map[string]domain.Group) error {
	for id, group := range after {
		previous, existed := before[id]
		if existed && previous.Name == group.Name && previous.ScopeKind == group.ScopeKind && previous.ScopeID == group.ScopeID && previous.Revision == group.Revision && sameStrings(previous.Members, group.Members) {
			continue
		}
		row := models.Group{GroupID: id, ScopeKind: group.ScopeKind, ScopeID: group.ScopeID, Name: group.Name, Revision: group.Revision}
		if !existed {
			if err := ormCAS(ctx, "Group changed", tx.orm.NewInsert().Model(&row).On("CONFLICT (group_id) DO NOTHING").Returning("NULL")); err != nil {
				return err
			}
		} else if err := ormCAS(ctx, "Group changed", tx.orm.NewUpdate().Model(&row).Column("scope_kind", "scope_id", "name", "revision").WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
		if !existed || !sameStrings(previous.Members, group.Members) {
			if _, err := tx.orm.NewDelete().Model((*models.GroupMember)(nil)).Where("group_id = ?", id).Exec(ctx); err != nil {
				return err
			}
			for _, user := range group.Members {
				row := models.GroupMember{GroupID: id, UserID: user}
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
		row := models.Group{GroupID: id}
		if err := ormCAS(ctx, "Group changed", tx.orm.NewDelete().Model(&row).WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
		if _, err := tx.orm.NewDelete().Model((*models.GroupMember)(nil)).Where("group_id = ?", id).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
