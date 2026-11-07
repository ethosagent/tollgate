package chain

import (
	"reflect"

	"github.com/ethosagent/tollgate/internal/methodset"
	"github.com/ethosagent/tollgate/internal/model"
)

var noopType = reflect.TypeFor[model.NoopHook]()

// overrides reports whether h implements method itself rather than through
// an embedded model.NoopHook. It runs once per hook, when the hook joins a
// Chain, so the request path never pays for it. It answers true whenever it
// cannot prove the method is NoopHook's no-op: a wrong "true" costs a no-op
// call, a wrong "false" would silently drop a hook. testkit applies the same
// rule through internal/methodset, so its harness offers the same stages.
func overrides(h model.Hook, method string) bool {
	return !methodset.PromotedFrom(reflect.TypeOf(h), method, noopType)
}
