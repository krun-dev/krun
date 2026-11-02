package control

import (
	"context"
	"errors"
	"time"
)

func Bootstrap(ctx context.Context, s *Store, user string) error {
	return s.Update(ctx, PartitionIAM, func(st *State) error {
		if st.Bootstrapped {
			return errors.New("bootstrap already completed")
		}
		u, ok := st.Users[user]
		if !ok || u.Status != "active" {
			return errors.New("an existing active local user_id is required")
		}
		id := randomID("role")
		st.Assignments[id] = Assignment{ID: id, SubjectKind: "user", SubjectID: user, Role: "system_admin", ScopeKind: "system", ScopeID: "system", Revision: 1}
		st.Bootstrapped = true
		st.Revision++
		st.RecordAudit(Audit{ID: randomID("audit"), Time: now(), Actor: user, Credential: "bootstrap", Action: "system.bootstrap", Resource: "system", Allowed: true, Revision: st.Revision})
		return nil
	})
}
func Invite(ctx context.Context, s *Store, source, subject string) error {
	if source == "" || subject == "" || len(subject) > 512 {
		return errors.New("source and stable subject required")
	}
	return s.Update(ctx, PartitionIAM, func(st *State) error {
		st.Invitations[source+"\x00"+subject] = now().Add(7 * 24 * time.Hour)
		st.Revision++
		return nil
	})
}
