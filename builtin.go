package tollgate

import (
	"context"
	"time"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/resolve"
)

// inlineSeams records which seams hold tollgate's own built-in extension:
// code that never blocks, does no I/O and ignores its context. Those run on
// the handler's goroutine under chain.Guard (recover, no deadline): the
// deadline goroutine, timer and two scheduler hand-offs that chain.Within
// costs buy nothing for them. A host extension always runs under Within.
type inlineSeams struct {
	auth, admission, resolver bool
}

// builtinSeams inspects opts once, in New, after defaults are applied. Only
// the exact built-in types count; a host type that wraps one does not.
func builtinSeams(opts Options) inlineSeams {
	var s inlineSeams
	switch opts.Auth.(type) {
	case NoAuth, *NoAuth:
		s.auth = true
	}
	switch opts.Admission.(type) {
	case Unlimited, *Unlimited:
		s.admission = true
	}
	_, s.resolver = opts.Providers.(*resolve.Resolver)
	return s
}

// guarded runs an extension call: inline under Guard for a built-in, else
// under Within with deadline d.
func guarded[T any](ctx context.Context, inline bool, d time.Duration, fn func(context.Context) (T, error)) (T, error) {
	if inline {
		return chain.Guard(ctx, fn)
	}
	return chain.Within(ctx, d, fn)
}
