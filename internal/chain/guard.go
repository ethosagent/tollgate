package chain

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
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
// its context ended first. It is a model.StatusError for 503: an Auth,
// Admission or ProviderResolver call that runs out of time is answered 503.
var ErrDeadline error = deadlineError{}

type deadlineError struct{}

func (deadlineError) Error() string   { return "chain: extension deadline exceeded" }
func (deadlineError) HTTPStatus() int { return http.StatusServiceUnavailable }

// Within runs fn under Guard with a context that ends after d. It is the
// extension guard: recover plus a deadline. fn runs on its own goroutine, so
// an extension that ignores its context cannot hold the caller: Within
// returns ErrDeadline at the deadline (or when ctx ends) and the late result
// is dropped. A result that arrives once the deadline has passed is late too,
// whatever fn returned. Callers hand fn private copies of anything it may write.
func Within[T any](ctx context.Context, d time.Duration, fn func(context.Context) (T, error)) (T, error) {
	return WithinLate(ctx, d, fn, nil)
}

// WithinLate is Within, and late (if not nil) receives, exactly once, the
// result of a call that missed its deadline once fn returns. It is for
// results that hold a resource, such as an Admission release func.
func WithinLate[T any](ctx context.Context, d time.Duration, fn func(context.Context) (T, error), late func(T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	const pending, finished, abandoned = 0, 1, 2
	var state atomic.Int32 // whoever moves it off pending owns the result
	done := make(chan result, 1)
	go func() {
		v, err := Guard(ctx, fn)
		if state.CompareAndSwap(pending, finished) {
			done <- result{v, err}
		} else if late != nil {
			late(v, err)
		}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		if state.CompareAndSwap(pending, abandoned) {
			var zero T
			return zero, ErrDeadline
		}
		r = <-done // it finished as the deadline hit: the result is ours
	}
	if ctx.Err() != nil { // returned, but only once its time was up: late all the same
		if late != nil {
			late(r.v, r.err)
		}
		var zero T
		return zero, ErrDeadline
	}
	return r.v, r.err
}
