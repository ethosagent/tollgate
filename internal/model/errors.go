package model

import (
	"errors"
	"math"
	"time"
)

// StatusError is an error that picks the HTTP status the proxy answers with.
// Any extension error that the core turns into a response (Authenticator,
// Admission, ProviderResolver) may implement it; errors.As finds it through
// wrapping. A status outside 400-599 is ignored and the seam's default is
// used (401 for Auth, 429 for Admission, 404 for the resolver). See
// docs/decisions/0008.
type StatusError interface {
	error
	HTTPStatus() int
}

// RetryAfterError is an optional companion to StatusError: an Admission
// error that implements it sets the Retry-After header (whole seconds,
// rounded up, at least 1).
type RetryAfterError interface {
	error
	RetryAfter() time.Duration
}

// StatusOf returns the status of the first StatusError in err's chain when
// it is a 4xx or 5xx code, else fallback. It is the single place the core
// maps an extension error to an HTTP status.
func StatusOf(err error, fallback int) int {
	var se StatusError
	if errors.As(err, &se) {
		if s := se.HTTPStatus(); s >= 400 && s <= 599 {
			return s
		}
	}
	return fallback
}

// RetryAfterOf returns the delay of the first RetryAfterError in err's chain.
func RetryAfterOf(err error) (time.Duration, bool) {
	var re RetryAfterError
	if errors.As(err, &re) {
		return re.RetryAfter(), true
	}
	return 0, false
}

// RetryAfterSeconds is the Retry-After value for an Admission error: the
// RetryAfterError delay in whole seconds, rounded up, at least 1.
func RetryAfterSeconds(err error) int {
	secs := 1
	if d, ok := RetryAfterOf(err); ok {
		secs = max(secs, int(math.Ceil(d.Seconds())))
	}
	return secs
}
