package control

import (
	"net/http"
	"sort"
	"time"
)

func (s *Server) users(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.read", systemTarget()); e != nil {
		return nil, 0, e
	}
	out := []User{}
	for _, u := range st.Users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return items(out)
}
func (s *Server) ensureAdmin(st *State) error {
	if !st.Bootstrapped {
		return nil
	}
	for _, u := range st.Users {
		if u.Status == "active" && s.sourceEnabled(u.Source) && isAdmin(st, u, "") {
			return nil
		}
	}
	return problem(409, "LAST_ADMIN", "At least one active system administrator must remain")
}
func (s *Server) userUpdate(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.update", systemTarget()); e != nil {
		return nil, 0, e
	}
	u, ok := st.Users[r.PathValue("user")]
	if !ok {
		return nil, 0, notFound()
	}
	var in struct {
		Status string `json:"status"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Status != "active" && in.Status != "disabled" {
		return nil, 0, problem(422, "INVALID_REQUEST", "Status must be active or disabled")
	}
	u.Status = in.Status
	u.AuthVersion++
	st.Users[u.ID] = u
	st.Revision++
	if e := s.ensureAdmin(st); e != nil {
		return nil, 0, e
	}
	return u, 200, nil
}
func (s *Server) inviteHandler(st *State, p Principal, r *http.Request) (any, int, error) {
	if e := s.require(st, p, r, "system.user.update", systemTarget()); e != nil {
		return nil, 0, e
	}
	var in struct {
		Source  string `json:"source_id"`
		Subject string `json:"subject"`
	}
	if e := body(r, &in); e != nil {
		return nil, 0, e
	}
	if !s.sourceEnabled(in.Source) || in.Subject == "" || len(in.Subject) > 512 {
		return nil, 0, problem(422, "INVALID_REQUEST", "Enabled source and stable subject required")
	}
	st.Invitations[in.Source+"\x00"+in.Subject] = now().Add(7 * 24 * time.Hour)
	st.Revision++
	return map[string]string{"status": "invited"}, 201, nil
}
