package control

import "net/http"

func (s *Server) resolveNativeRoute(r *http.Request) error {
	org := r.PathValue("org")
	if org == "" {
		return nil
	}
	if err := s.resolveOrganizationPath(r, org); err != nil {
		return err
	}
	org = r.PathValue("org")
	if resource := r.PathValue("resource"); resource != "" {
		id, err := s.nativeResourceID(r, org, resourceKind(r), resource)
		if err != nil {
			return err
		}
		r.SetPathValue("resource", id)
	}
	if r.Pattern == "GET /api/v1/orgs/{org}/workers" {
		if locator := r.URL.Query().Get("worker"); locator != "" {
			id, err := s.nativeResourceID(r, org, "worker", locator)
			if err != nil {
				return err
			}
			r.SetPathValue("workerFilter", id)
		}
	}
	return nil
}

func (s *Server) nativeResourceID(r *http.Request, org, kind, ref string) (string, error) {
	id, err := s.Store.ResourceID(r.Context(), org, kind, ref, true)
	if isProblem(err, "RESOURCE_NOT_FOUND") {
		return ref, nil
	}
	return id, err
}

func (s *Server) resolveCloudflareAccount(r *http.Request) error {
	account := r.PathValue("account")
	if account == "" || r.PathValue("org") != "" {
		return nil
	}
	return s.resolveOrganizationPath(r, account)
}

// Leave unknown references for the handler to reject after authenticating.
func (s *Server) resolveOrganizationPath(r *http.Request, ref string) error {
	id, err := s.Store.OrganizationID(r.Context(), ref)
	if isProblem(err, "RESOURCE_NOT_FOUND") {
		id, err = ref, nil
	}
	if err == nil {
		r.SetPathValue("org", id)
	}
	return err
}

func workerFilter(r *http.Request) string {
	if id := r.PathValue("workerFilter"); id != "" {
		return id
	}
	return r.URL.Query().Get("worker_id")
}

func resourcePath(st *State, org, collection, locator string) string {
	shortName := st.Organizations[org].ShortName
	if shortName == "" || locator == "" {
		return ""
	}
	return shortName + "/" + collection + "/" + locator
}

func presentResource(st *State, resource Resource) Resource {
	resource.ResourcePath = resourcePath(st, resource.Organization, resource.Kind+"s", resource.Locator)
	return resource
}
