package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func TestPostgresOrganizationInvitationLimitAndPagination(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *domain.State) error {
		st.Organizations["org"] = domain.Organization{ID: "org", Name: "Invitations", Revision: 1}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			for attempt := 0; attempt < 30; attempt++ {
				st, err := store.OrganizationInvitationContext(ctx, "org")
				if err != nil {
					t.Error(err)
					return
				}
				in := domain.OrganizationInvitation{
					ID: fmt.Sprintf("invite-%02d", i), Organization: "org", Creator: "creator", Role: "member",
					TokenHash: fmt.Sprintf("hash-%02d", i), MaxUses: 1, Created: now(), Expires: now().Add(time.Hour), Revision: 1,
				}
				err = store.CreateOrganizationInvitation(ctx, in, st.Organizations["org"].Revision, domain.Audit{ID: fmt.Sprintf("audit-%02d", i), Time: now()})
				if err == nil {
					return
				}
				var problem *domain.APIError
				if !errors.As(err, &problem) {
					t.Error(err)
					return
				}
				if problem.Code == "INVITATION_LIMIT_REACHED" {
					return
				}
				if problem.Code != "REVISION_CONFLICT" {
					t.Error(err)
					return
				}
			}
			t.Error("concurrent create failed to converge")
		}(i)
	}
	close(start)
	group.Wait()
	items, err := store.ListOrganizationInvitations(ctx, "org", "", 100)
	if err != nil || len(items) != 10 {
		t.Fatalf("active limit: %d %v", len(items), err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := store.ListOrganizationInvitations(ctx, "org", cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, in := range page {
			if seen[in.ID] {
				t.Fatal("duplicate pagination item", in.ID)
			}
			seen[in.ID] = true
		}
		cursor = page[len(page)-1].ID
	}
	if len(seen) != 10 {
		t.Fatal("pagination omitted invitations", len(seen))
	}
	other, err := store.ListOrganizationInvitations(ctx, "other-org", items[0].ID, 100)
	if err != nil || len(other) != 0 {
		t.Fatal("cross organization cursor leak", other, err)
	}
	if err := store.RevokeOrganizationInvitation(ctx, "org", items[0].ID, domain.Audit{ID: "revoke", Time: now()}); err != nil {
		t.Fatal(err)
	}
	st, err := store.OrganizationInvitationContext(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	in := domain.OrganizationInvitation{ID: "replacement", Organization: "org", Role: "viewer", TokenHash: "replacement-hash", MaxUses: 1, Created: now(), Expires: now().Add(time.Hour), Revision: 1}
	if err := store.CreateOrganizationInvitation(ctx, in, st.Organizations["org"].Revision, domain.Audit{ID: "replacement-audit", Time: now()}); err != nil {
		t.Fatal("revocation did not free active link slot", err)
	}
}

func TestPostgresOrganizationInvitationRetention(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	org := models.Organization{OrgID: "org", Name: "Retention", Revision: 1}
	if _, err := store.orm.NewInsert().Model(&org).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		id, org string
		expires time.Time
	}{
		{"old", "org", now().Add(-8 * 24 * time.Hour)},
		{"recent-expired", "org", now().Add(-time.Hour)},
		{"active", "org", now().Add(time.Hour)},
		{"orphan", "deleted-org", now().Add(time.Hour)},
	} {
		in := models.OrgInvitationLink{
			InvitationID: entry.id, OrgID: entry.org, CreatorUserID: "creator", Role: "member", TokenHash: entry.id,
			MaxUses: 1, UseCount: 1, CreatedAt: entry.expires.Add(-7 * 24 * time.Hour), ExpiresAt: entry.expires, Revision: 1,
		}
		if _, err := store.orm.NewInsert().Model(&in).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		redemption := models.OrgInvitationRedemption{InvitationID: entry.id, UserID: "recipient", JoinedAt: in.CreatedAt}
		if _, err := store.orm.NewInsert().Model(&redemption).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	orphan := models.OrgInvitationRedemption{InvitationID: "missing", UserID: "recipient", JoinedAt: now()}
	if _, err := store.orm.NewInsert().Model(&orphan).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "orphan", "missing", "recent-expired", "active"} {
		_, found, err := store.OrganizationInvitation(ctx, id, "")
		redeemed, redemptionErr := store.OrganizationInvitationRedeemed(ctx, id, "recipient")
		expected := id == "recent-expired" || id == "active"
		if err != nil || redemptionErr != nil || found != expected || redeemed != expected {
			t.Errorf("retention %s: link=%t redemption=%t errors=%v/%v", id, found, redeemed, err, redemptionErr)
		}
	}
}
