package storage

import (
	"context"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

// RedeemOrganizationInvitation atomically claims one place and creates the
// membership, direct role and redemption. False means another link already
// created this membership; the entire attempted claim is then rolled back.
func (s *Store) RedeemOrganizationInvitation(ctx context.Context, in domain.OrganizationInvitation, user domain.User, assignment domain.Assignment, audit domain.Audit) (bool, error) {
	if s.pool == nil {
		return s.redeemMemoryInvitation(in, user, assignment, audit)
	}
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	row := invitationModel(in)
	if err := ormCAS(ctx, "Invitation changed", tx.orm.NewUpdate().Model(&row).
		Set("use_count = use_count + 1").Set("revision = revision + 1").WherePK().
		Where("revision = ? AND revoked_at IS NULL AND expires_at > clock_timestamp() AND use_count < max_uses", in.Revision)); err != nil {
		return false, err
	}
	validUser, err := tx.orm.NewSelect().Model((*models.User)(nil)).
		Where("user_id = ? AND status = 'active' AND auth_version = ? AND source = ? AND email = ? AND email_verified = ?",
			user.ID, user.AuthVersion, user.Source, user.Email, user.EmailVerified).Exists(ctx)
	if err != nil {
		return false, err
	}
	if !validUser {
		return false, invitationConflict()
	}
	redemption := models.OrgInvitationRedemption{InvitationID: in.ID, UserID: user.ID, JoinedAt: now()}
	result, err := tx.orm.NewInsert().Model(&redemption).On("CONFLICT DO NOTHING").Returning("NULL").Exec(ctx)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, domain.Problem(409, "INVITATION_ALREADY_USED", "A new invitation is required after membership removal")
	}
	member := models.Member{OrgID: in.Organization, UserID: user.ID}
	result, err = tx.orm.NewInsert().Model(&member).On("CONFLICT DO NOTHING").Returning("NULL").Exec(ctx)
	if err != nil {
		return false, err
	}
	count, err = result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	if err := writeAssignments(ctx, tx, map[string]domain.Assignment{}, map[string]domain.Assignment{assignment.ID: assignment}); err != nil {
		return false, err
	}
	if err := writeInvitationAudit(ctx, tx, audit); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) redeemMemoryInvitation(in domain.OrganizationInvitation, user domain.User, assignment domain.Assignment, audit domain.Audit) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.organizationInvitations[in.ID]
	if !found || current.Revision != in.Revision || current.WithStatus(now()).Status != "active" || s.memory.Users[user.ID] != user {
		return false, invitationConflict()
	}
	if s.invitationRedemptions[in.ID][user.ID] {
		return false, domain.Problem(409, "INVITATION_ALREADY_USED", "A new invitation is required after membership removal")
	}
	if _, exists := s.memory.Assignments[assignment.ID]; exists {
		return false, invitationConflict()
	}
	key := domain.MemberKey(in.Organization, user.ID)
	if _, found := s.memory.Members[key]; found {
		return false, nil
	}
	if s.invitationRedemptions == nil {
		s.invitationRedemptions = map[string]map[string]bool{}
	}
	if s.invitationRedemptions[in.ID] == nil {
		s.invitationRedemptions[in.ID] = map[string]bool{}
	}
	s.invitationRedemptions[in.ID][user.ID] = true
	current.UseCount++
	current.Revision++
	s.organizationInvitations[in.ID] = current
	s.memory.Members[key] = domain.Member{Organization: in.Organization, User: user.ID}
	s.memory.Assignments[assignment.ID] = assignment
	s.recordInvitationMemoryAudit(audit)
	return true, nil
}
