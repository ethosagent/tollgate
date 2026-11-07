package tollgate

import (
	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// newFinalizers builds the Finalizer list of a Proxy from validated Options.
// Each Decide runs under Limits.FinalizerDeadline and all of a call's under
// FinalizerCallDeadline; FinalizerBudget and RecoveryCeiling count recovery
// calls per finalizer and per call.
func newFinalizers(o Options) *chain.Finalizers {
	l := o.Limits
	return chain.NewFinalizers(o.Finalizers, o.Logger, chain.FinalizeLimits{
		Decide: l.FinalizerDeadline, Total: l.FinalizerCallDeadline, Budget: l.FinalizerBudget, Ceiling: l.RecoveryCeiling})
}

// finalize returns the terminal-event barrier (forward.Finalize) of one
// streamed call, or nil: no finalizer is registered, the call's wire has no
// normaliser (a pass-through provider), or it asks for several choices
// (the terminal region and the recovery splice assume one; ADR 0013), so
// nothing is held. With nil the stream takes the plain tee and costs
// nothing extra (STR-6).
func (p *Proxy) finalize(o *outcome, wire normalise.Wire) *forward.Finalize {
	if p.fin.Empty() || o.call.Choices > 1 {
		return nil
	}
	term, err := normalise.TerminalFor(o.call.Provider.Wire)
	if err != nil {
		return nil
	}
	return &forward.Finalize{Wire: wire, Term: term, Decide: p.fin.Begin(o.call).Decide}
}
