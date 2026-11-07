package tollgate

import (
	"context"
	"reflect"
	"time"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/methodset"
)

// inlineSeams records which seams hold an extension that declared
// InlineSafe (ADR 0015): its method never blocks, so it runs on the
// handler's goroutine under chain.Guard (recover, no deadline). The deadline
// goroutine, timer and two scheduler hand-offs that chain.Within costs buy
// nothing for it. Every other extension runs under Within.
type inlineSeams struct {
	auth, admission, resolver bool
}

// inlineSeamsOf inspects opts once, in New, after defaults are applied.
func inlineSeamsOf(opts Options) inlineSeams {
	return inlineSeams{
		auth:      inlineSafe(opts.Auth),
		admission: inlineSafe(opts.Admission),
		resolver:  inlineSafe(opts.Providers),
	}
}

// inlineSafe reports whether ext implements InlineSafe and its own type
// declares the method: promotion from an embedded marked type does not
// count, since the outer type may override the seam method with code that
// blocks.
func inlineSafe(ext any) bool {
	_, ok := ext.(InlineSafe)
	return ok && methodset.Declares(reflect.TypeOf(ext), "InlineSafe")
}

// guarded runs an extension call: inline under Guard for an InlineSafe
// extension, else under Within with deadline d.
func guarded[T any](ctx context.Context, inline bool, d time.Duration, fn func(context.Context) (T, error)) (T, error) {
	if inline {
		return chain.Guard(ctx, fn)
	}
	return chain.Within(ctx, d, fn)
}
