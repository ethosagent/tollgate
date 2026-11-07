// Package statickeys is a tollgate.Authenticator backed by a fixed table of
// secret keys. A request carries its key in one header (default
// tollgate.DefaultHeaderNames().TenantKey, "x-proxy-key"); the key selects a
// tenant and its policy (AllowedProviders, AllowedModels, AllowedUpstreams).
//
// A missing key is a 401 and an unknown key a 403; both errors implement
// tollgate.StatusError, so the core answers with that status. Keys are
// compared in constant time over every entry, are never logged, and never
// appear in an error message. Config is validated once, in New.
//
// Wiring:
//
//	auth, err := statickeys.New(statickeys.Config{
//		Keys: map[string]statickeys.Tenant{
//			"sk-team-a": {ID: "team-a", AllowedProviders: []string{"openai"}},
//			"sk-team-b": {ID: "team-b", AllowedModels: []string{"claude-sonnet-4"}},
//		},
//	})
//	if err != nil { /* bad config */ }
//	p, err := tollgate.New(tollgate.Options{Auth: auth})
//
// Load keys from the environment or a secret store in the host; this package
// never reads files or env itself.
package statickeys
