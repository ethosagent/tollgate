package chain

import (
	"errors"
	"net/http"
)

// ErrPanic is returned by Guard when the guarded extension call panicked. It
// is a model.StatusError for 500, so model.StatusOf maps it without a special
// case. It never carries the panic value, which may hold request data.
var ErrPanic error = panicError{}

type panicError struct{}

func (panicError) Error() string   { return "chain: extension panicked" }
func (panicError) HTTPStatus() int { return http.StatusInternalServerError }

// Guard runs fn and turns a panic into ErrPanic with the zero T, so a broken
// extension degrades to an error answer instead of a dropped connection
// (non-negotiable 2). It adds no deadline: the seam's context carries one.
// Milestone 005 builds the hook guard (recover plus Limits deadline) on it.
func Guard[T any](fn func() (T, error)) (v T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			v, err = zero, ErrPanic
		}
	}()
	return fn()
}

// IsPanic reports whether err came from a recovered panic.
func IsPanic(err error) bool { return errors.Is(err, ErrPanic) }
