package testkit

import (
	"reflect"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/internal/methodset"
)

// The core offers a Hook only the stages it implements itself: a method
// promoted from an embedded tollgate.NoopHook is skipped (and, for OnEvent
// and After, no event tap is built). The harness must offer the same stages,
// or a Recording would show calls production never makes. Both apply the
// one rule in internal/methodset (internal/chain/detect.go is the core's
// side); TestRun_RecordsOnlyOverriddenStages checks the result end to end.

var noopType = reflect.TypeFor[tollgate.NoopHook]()

// overrides reports whether h implements method itself rather than through
// an embedded NoopHook. When in doubt it answers true.
func overrides(h tollgate.Hook, method string) bool {
	return !methodset.PromotedFrom(reflect.TypeOf(h), method, noopType)
}
