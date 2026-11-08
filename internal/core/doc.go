// Package core is the proxy engine behind the root tollgate package: the
// Proxy pipeline (auth, admission, resolve, read, forward-and-tee, record),
// the hook and Finalizer wiring, the default session strategy wiring, option
// defaults and validation, and the admin endpoints.
//
// The root package holds only the public API (Options, Limits, HeaderNames,
// Proxy and its methods, the model aliases) and delegates to this package:
// root New copies tollgate.Options into core.Options and wraps *core.Proxy.
// See docs/decisions/0016.
//
// Dependency rule D3: core imports the standard library and internal/* only,
// never the root, ext, testkit or cmd. Its tests follow the same rule: they
// replay internal/fixtures through a local fake upstream instead of testkit.
package core
