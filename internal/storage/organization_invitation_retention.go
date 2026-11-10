package storage

import (
	"context"
	"time"
)

func (s *Store) retainOrganizationInvitations(ctx context.Context) error {
	cutoff := now().Add(-7 * 24 * time.Hour)

	_, err := s.pool.Exec(ctx, `WITH victims AS MATERIALIZED (
		SELECT invitation_id FROM krun_org_invitation_links i
		WHERE expires_at <= $1 OR NOT EXISTS (SELECT 1 FROM krun_organizations o WHERE o.org_id=i.org_id)
		ORDER BY expires_at LIMIT 1000
	), redemptions AS (
		DELETE FROM krun_org_invitation_redemptions WHERE invitation_id IN (SELECT invitation_id FROM victims)
	)
	DELETE FROM krun_org_invitation_links WHERE invitation_id IN (SELECT invitation_id FROM victims)`, cutoff)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM krun_org_invitation_redemptions WHERE (invitation_id,user_id) IN (
		SELECT r.invitation_id,r.user_id FROM krun_org_invitation_redemptions r
		WHERE NOT EXISTS (SELECT 1 FROM krun_org_invitation_links i WHERE i.invitation_id=r.invitation_id) LIMIT 1000
	)`)
	return err
}
