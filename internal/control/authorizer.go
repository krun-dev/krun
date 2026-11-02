package control

import (
	"github.com/krun-dev/krun/internal/authorization"
)

// These aliases keep the existing control API while the authorization engine
// lives independently of HTTP, persistence and Workers orchestration.
type Principal = authorization.Principal

type Target = authorization.Target

type Decision = authorization.Decision

var (
	Authorize      = authorization.Authorize
	resolve        = authorization.Resolve
	validatePolicy = authorization.ValidatePolicy
	isAdmin        = authorization.IsAdmin
)
