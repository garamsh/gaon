package api

import "fmt"

// Reason says which failure an APIError reports.
type Reason string

// The reasons an API call or a watch can fail with.
const (
	ReasonConflict      Reason = "Conflict"
	ReasonNotFound      Reason = "NotFound"
	ReasonAlreadyExists Reason = "AlreadyExists"
	ReasonInvalid       Reason = "Invalid"
	ReasonExpired       Reason = "Expired"
	ReasonUnauthorized  Reason = "Unauthorized"
	ReasonForbidden     Reason = "Forbidden"
)

// APIError is the error object of the API: the body of a failed response and
// the object of a watch Error event. Match one by reason with errors.Is
// against the sentinels below.
type APIError struct {
	Code    int    `json:"code"`
	Reason  Reason `json:"reason"`
	Message string `json:"message"`
}

// The sentinels errors.Is matches an APIError against, by Reason only.
var (
	ErrConflict      = NewAPIError(ReasonConflict, "conflict")
	ErrNotFound      = NewAPIError(ReasonNotFound, "not found")
	ErrAlreadyExists = NewAPIError(ReasonAlreadyExists, "already exists")
	ErrInvalid       = NewAPIError(ReasonInvalid, "invalid")
	ErrExpired       = NewAPIError(ReasonExpired, "expired")
	ErrUnauthorized  = NewAPIError(ReasonUnauthorized, "unauthorized")
	ErrForbidden     = NewAPIError(ReasonForbidden, "forbidden")
)

// NewAPIError returns the APIError for reason with its HTTP status code.
func NewAPIError(reason Reason, format string, args ...any) *APIError {
	return &APIError{Code: reason.httpStatus(), Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Reason, e.Message)
}

// Is reports whether target is an APIError with the same Reason.
func (e *APIError) Is(target error) bool {
	t, ok := target.(*APIError)
	return ok && t.Reason == e.Reason
}

func (r Reason) httpStatus() int {
	switch r {
	case ReasonUnauthorized:
		return 401
	case ReasonForbidden:
		return 403
	case ReasonConflict, ReasonAlreadyExists:
		return 409
	case ReasonNotFound:
		return 404
	case ReasonInvalid:
		return 422
	case ReasonExpired:
		return 410
	default:
		return 500
	}
}

func invalid(format string, args ...any) error {
	return NewAPIError(ReasonInvalid, format, args...)
}
