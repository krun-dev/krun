package control

import (
	"context"

	"github.com/krun-dev/krun/internal/iam"
)

func Bootstrap(ctx context.Context, s *Store, user string) error {
	return s.Update(ctx, PartitionIAM, func(st *State) error { return iam.Bootstrap(st, user) })
}

func Invite(ctx context.Context, s *Store, source, subject string) error {
	return s.Update(ctx, PartitionIAM, func(st *State) error { return iam.AddInvitation(st, source, subject) })
}
