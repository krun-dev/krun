package provider

type ErrorKind string

const (
	InvalidConfiguration ErrorKind = "invalid_configuration"
	InvalidScope         ErrorKind = "invalid_scope"
	AuthenticationFailed ErrorKind = "authentication_failed"
	PermissionDenied     ErrorKind = "permission_denied"
	Throttled            ErrorKind = "throttled"
	Unavailable          ErrorKind = "unavailable"
	InsufficientValidity ErrorKind = "insufficient_validity"
)

// No upstream message, URL, response body or wrapped SDK error is retained.
type Error struct {
	Kind      ErrorKind
	RequestID string
}

func (e *Error) Error() string { return "storage credentials: " + string(e.Kind) }

func (e *Error) Retryable() bool { return e.Kind == Unavailable || e.Kind == Throttled }
