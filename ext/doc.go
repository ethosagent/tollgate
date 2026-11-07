// Package ext is the home of built-in and example extensions, one
// sub-package each (ext/auth/statickeys, ext/metrics/postgres, ext/monitors,
// ...). Extensions are ordinary Go packages built only on the public
// tollgate API. They never import internal/ (internal/archtest, rule D5),
// which proves the public surface is enough to write a real extension.
package ext
