package audit

import (
	"time"

	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

func Record(st *domain.State, p authorization.Principal, requestID, action, target string, allowed bool) {
	key := ""
	if p.Key != nil {
		key = p.Key.ID
	}
	org := ""
	if _, ok := st.Organizations[target]; ok {
		org = target
	}
	if res, ok := st.Resources[target]; ok {
		org = res.Organization
	}
	if key, ok := st.Keys[target]; ok {
		org = key.Organization
	}
	st.RecordAudit(domain.Audit{ID: securetoken.NewID("audit"), Time: time.Now().UTC(), Actor: p.User.ID, Credential: p.Kind, KeyID: key, Action: action, Resource: target, Organization: org, Allowed: allowed, RequestID: requestID, Revision: st.Revision})

}
