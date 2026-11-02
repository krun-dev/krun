package authorization

import "github.com/krun-dev/krun/internal/domain"

// Check conceals resources the caller cannot read, while returning a denial for
// visible resources whose requested action is forbidden.
func Check(st *domain.State, p Principal, action string, t Target) (Decision, error) {
	d, e := Authorize(st, p, action, t)
	if e != nil {
		return Decision{}, e
	}
	if !d.Allowed {
		readAction := t.Kind + ".read"
		if t.Kind == "api_key" {
			readAction = "apiKey.read"
		}
		if t.Kind == "system" {
			readAction = "system.role.read"
		}
		visible, err := Authorize(st, p, readAction, t)
		if err != nil {
			return Decision{}, err
		}
		if !visible.Allowed {
			return Decision{}, domain.NotFound()
		}
	}

	return d, nil
}
