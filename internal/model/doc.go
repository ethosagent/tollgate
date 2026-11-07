// Package model holds tollgate's neutral, provider-independent model: the
// types every stage of a call shares (Tenant, Session, Call, Event, Metric,
// Provider, Response) and the extension interfaces (Authenticator, Admission,
// ProviderResolver, SessionStrategy, Hook, Finalizer, MetricSink).
//
// It lives here, not in the root package, so the internal engine packages can
// use it without an import cycle. The root package re-exports every name as a
// type alias, so extension authors import only
// "github.com/ethosagent/tollgate". See docs/decisions/0003.
//
// Dependency law (internal/archtest, rule D2): this package imports the
// standard library only.
package model
