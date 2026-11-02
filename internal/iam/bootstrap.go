package iam

import (
	"errors"
	"time"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

// Bootstrap is the local administrative entry point; it can only run once.
func Bootstrap(st *domain.State, user string) error {
	if st.Bootstrapped {
		return errors.New("bootstrap already completed")
	}
	u, ok := st.Users[user]
	if !ok || u.Status != "active" {
		return errors.New("an existing active local user_id is required")
	}
	id := securetoken.NewID("role")
	st.Assignments[id] = domain.Assignment{ID: id, SubjectKind: "user", SubjectID: user, Role: "system_admin", ScopeKind: "system", ScopeID: "system", Revision: 1}
	st.Bootstrapped = true
	st.Revision++
	st.RecordAudit(domain.Audit{ID: securetoken.NewID("audit"), Time: time.Now().UTC(), Actor: user, Credential: "bootstrap", Action: "system.bootstrap", Resource: "system", Allowed: true, Revision: st.Revision})
	return nil
}

// AddInvitation is also used by the local CLI, before an identity source is
// enabled. HTTP callers validate the source against application configuration.
func AddInvitation(st *domain.State, source, subject string) error {
	if source == "" || subject == "" || len(subject) > 512 {
		return errors.New("source and stable subject required")
	}
	st.Invitations[source+"\x00"+subject] = time.Now().UTC().Add(7 * 24 * time.Hour)
	st.Revision++
	return nil
}
