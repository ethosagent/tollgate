// Package tollgate is an embeddable LLM proxy with extension points.
//
// The core resolves the upstream, identifies the session, normalises the
// call, forwards and tees the stream, and emits a metric. Everything else
// (auth, admission, monitors, routing, memory, billing) is an extension that
// implements one or more of the interfaces in this package.
//
// Guarantees the core makes:
//
//   - Transparent: body, model and upstream credentials pass through
//     unchanged unless an extension changes them.
//   - Client-first streaming: bytes reach the client as they arrive; no
//     extension can stall the stream.
//   - Isolation: every extension runs under a panic guard and a deadline; a
//     broken extension degrades to pass-through.
//   - One neutral model: extensions see Call and Event, not provider JSON.
//   - Session identity: every call is attributed to a Session.
//
// Embed it:
//
//	p, err := tollgate.New(tollgate.Options{Auth: myAuth{}})
//	if err != nil { ... }
//	p.Use(myHook{})
//	http.ListenAndServe(":8080", p.Handler())
//
// Or run the binary in cmd/tollgated. See ARCHITECTURE.md.
package tollgate
