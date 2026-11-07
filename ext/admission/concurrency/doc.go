// Package concurrency is a tollgate.Admission that caps in-flight calls per
// tenant. Each tenant has its own limit (DefaultMax, or a PerTenant
// override), so a saturated tenant never takes slots from another.
//
// A call over the limit waits up to QueueTimeout for a slot, honouring its
// context. After the timeout Acquire fails with an error that implements
// tollgate.StatusError (429) and tollgate.RetryAfterError, so the core answers
// 429 with a Retry-After header. The release function frees exactly one
// slot, however often it is called. Idle tenants hold no memory.
//
// Wiring:
//
//	adm, err := concurrency.New(concurrency.Config{
//		DefaultMax:   8,
//		PerTenant:    map[string]int{"team-a": 32},
//		QueueTimeout: 2 * time.Second,
//		RetryAfter:   5 * time.Second,
//	})
//	if err != nil { /* bad config */ }
//	p, err := tollgate.New(tollgate.Options{Auth: auth, Admission: adm})
//
// Limits are per process; running several proxies multiplies them. Priority
// and aging are out of scope: waiters are served in the order the runtime
// wakes them.
package concurrency
