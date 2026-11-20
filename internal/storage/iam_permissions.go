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
			Locator:    resource.Locator,
			Name:       resource.Name,
			Revision:   resource.Revision,
			CreatedAt:  resource.Created,
		}
		if !existed {
			if resource.Kind == "worker" && resource.Locator != "" {
				if err := claimWorkerAddress(ctx, tx, resource.Locator, "worker", resource.ID); err != nil {
					return err
				}
			}
			if err := insertLocator(ctx, "Resource locator already exists", tx.orm.NewInsert().Model(&row).Column("resource_id", "kind", "org_id", "locator", "name", "revision", "created_at").On("CONFLICT DO NOTHING").Returning("NULL")); err != nil {
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
		if previous.Kind == "worker" {
			if err := releaseWorkerAddress(ctx, tx, "worker", id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE krun_worker_address_claims SET expires_at=now()+interval '31 days'
				WHERE owner_kind='preview' AND split_part(owner_id,'/',1)=$1 AND expires_at IS NULL`, id); err != nil {
				return err
			}
		}
		if err := ormCAS(ctx, "Resource changed", tx.orm.NewDelete().Model(&row).WherePK().Where("revision = ?", previous.Revision)); err != nil {
			return err
		}
	}
	return nil
}

func sameResource(a, b domain.Resource) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.Organization == b.Organization &&
		a.Locator == b.Locator && a.Name == b.Name && a.Revision == b.Revision && a.Created.Equal(b.Created)
}
