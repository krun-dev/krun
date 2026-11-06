package storage

import (
	"context"
	"encoding/json"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func writeAssignments(ctx context.Context, tx ormTransaction, before, after map[string]domain.Assignment) error {
	for id, assignment := range after {
		previous, existed := before[id]
		if existed && sameAssignment(previous, assignment) {
			continue
		}
		conditions, err := json.Marshal(assignment.Conditions)
		if err != nil {
			return err
		}
		row := models.Assignment{AssignmentID: id, SubjectKind: assignment.SubjectKind, SubjectID: assignment.SubjectID, Role: assignment.Role, ScopeKind: assignment.ScopeKind, ScopeID: assignment.ScopeID, Conditions: conditions, Revision: assignment.Revision}
		if !existed {
			if err := ormCAS(ctx, "Assignment changed", tx.orm.NewInsert().Model(&row).On("CONFLICT (assignment_id) DO NOTHING").Returning("NULL")); err != nil {
				return err
			}
		} else if err := ormCAS(ctx, "Assignment changed", tx.orm.NewUpdate().Model(&row).Column("subject_kind", "subject_id", "role", "scope_kind", "scope_id", "conditions", "revision").WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
	}
	for id, previous := range before {
		if _, exists := after[id]; exists {
			continue
		}
		row := models.Assignment{AssignmentID: id}
		if err := ormCAS(ctx, "Assignment changed", tx.orm.NewDelete().Model(&row).WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
	}
	return nil
}
func writeResources(ctx context.Context, tx ormTransaction, before, after map[string]domain.Resource) error {
	for id, resource := range after {
		previous, existed := before[id]
		if existed && sameResource(previous, resource) {
			continue
		}
		row := models.Resource{
			ResourceID: id,
			Kind:       resource.Kind,
			OrgID:      resource.Organization,
			Name:       resource.Name,
			Revision:   resource.Revision,
			CreatedAt:  resource.Created,
		}
		if !existed {
			if err := ormCAS(ctx, "Resource changed", tx.orm.NewInsert().Model(&row).Column("resource_id", "kind", "org_id", "name", "revision", "created_at").On("CONFLICT (resource_id) DO NOTHING").Returning("NULL")); err != nil {
				return err
			}
		} else if err := ormCAS(ctx, "Resource changed", tx.orm.NewUpdate().Model(&row).Column("kind", "org_id", "name", "revision").WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
	}
	for id, previous := range before {
		if _, exists := after[id]; exists {
			continue
		}
		row := models.Resource{ResourceID: id}
		if err := ormCAS(ctx, "Resource changed", tx.orm.NewDelete().Model(&row).WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
	}
	return nil
}

func sameResource(a, b domain.Resource) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.Organization == b.Organization &&
		a.Name == b.Name && a.Revision == b.Revision && a.Created.Equal(b.Created)
}
