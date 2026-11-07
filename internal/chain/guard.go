package chain

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrPanic is returned by Guard when the guarded extension call panicked. It
// is a model.StatusError for 500, so model.StatusOf maps it without a special
// case. It never carries the panic value, which may hold request data.
var ErrPanic error = panicError{}

type panicError struct{}

func (panicError) Error() string   { return "chain: extension panicked" }
func (panicError) HTTPStatus() int { return http.StatusInternalServerError }

// Guard runs fn(ctx) and turns a panic into ErrPanic with the zero T, so a broken
// extension degrades to an error answer instead of a dropped connection
// (non-negotiable 2). It adds no deadline: the seam's context carries one.
// Within adds the deadline.
func Guard[T any](ctx context.Context, fn func(context.Context) (T, error)) (v T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			v, err = zero, ErrPanic
		}
	}()
	return fn(ctx)
}

// IsPanic reports whether err came from a recovered panic.
func IsPanic(err error) bool { return errors.Is(err, ErrPanic) }

// ErrDeadline is returned by Within when the call did not finish in time or
// its context ended first.
var ErrDeadline = errors.New("chain: hook deadline exceeded")

// Within runs fn under Guard with a context that ends after d. It is the hook
// guard: recover plus a deadline. fn runs on its own goroutine, so a hook that
// ignores its context cannot hold the caller: Within returns ErrDeadline at the
// deadline (or when ctx ends) and the late result is dropped. Callers hand fn
// private copies of anything it may write.
func Within[T any](ctx context.Context, d time.Duration, fn func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1) // buffered: an abandoned fn still exits
	go func() {
		v, err := Guard(ctx, fn)
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ErrDeadline
	}
}
