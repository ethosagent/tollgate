package tollgate

import (
	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// newFinalizers builds the Finalizer list of a Proxy from validated Options.
// Decide runs under Limits.HookChainDeadline; FinalizerBudget and
// RecoveryCeiling count recovery calls per finalizer and per call.
func newFinalizers(o Options) *chain.Finalizers {
	return chain.NewFinalizers(o.Finalizers, o.Logger, chain.FinalizeLimits{
		Decide: o.Limits.HookChainDeadline, Budget: o.Limits.FinalizerBudget, Ceiling: o.Limits.RecoveryCeiling})
}

// finalize returns the terminal-event barrier (forward.Finalize) of one
// streamed call, or nil: no finalizer is registered, or the call's wire has
// no normaliser (a pass-through provider), so nothing is held. With nil the
// stream takes the plain tee and costs nothing extra (STR-6).
func (p *Proxy) finalize(o *outcome, wire normalise.Wire) *forward.Finalize {
	if p.fin.Empty() {
		return nil
	}
	term, err := normalise.TerminalFor(o.call.Provider.Wire)
	if err != nil {
		return nil
	}
	return &forward.Finalize{Wire: wire, Term: term, Decide: p.fin.Begin(o.call).Decide}
}
