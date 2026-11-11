package domain

// ErrorKind describes the protocol-independent category of a business error.
// HTTP adapters translate it into a status code; storage and business modules
// no longer need to choose HTTP semantics.
type ErrorKind uint8

const (
	// ErrorHTTP keeps the explicit status for protocol-owned failures such as
	// malformed requests, authentication, rate limiting and payload limits.
	ErrorHTTP ErrorKind = iota
	ErrorInvalid
	ErrorConflict
	ErrorUnavailable
	ErrorForbidden
	ErrorNotFound
)

type APIError struct {
	Kind          ErrorKind
	Status        int
	Code, Message string
}

func (e *APIError) HTTPStatus() int {
	switch e.Kind {
	case ErrorInvalid:
		return 422
	case ErrorConflict:
		return 409
	case ErrorUnavailable:
		return 503
	case ErrorForbidden:
		return 403
	case ErrorNotFound:
		return 404
	default:
		return e.Status
	}
}

func Problem(status int, code, message string) error {
	return &APIError{Kind: ErrorHTTP, Status: status, Code: code, Message: message}
}

// Invalid reports a validated business input rejection.
func Invalid(code, message string) error {
	return &APIError{Kind: ErrorInvalid, Status: 422, Code: code, Message: message}
}

// Conflict reports an optimistic-concurrency or state-transition conflict.
func Conflict(code, message string) error {
	return &APIError{Kind: ErrorConflict, Status: 409, Code: code, Message: message}
}

// Unavailable reports a dependency or platform capability that is not ready.
func Unavailable(code, message string) error {
	return &APIError{Kind: ErrorUnavailable, Status: 503, Code: code, Message: message}
}

func ForbiddenCode(code, message string) error {
	return &APIError{Kind: ErrorForbidden, Status: 403, Code: code, Message: message}
}

func NotFoundCode(code, message string) error {
	return &APIError{Kind: ErrorNotFound, Status: 404, Code: code, Message: message}
}
