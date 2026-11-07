package metrics

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/leakcheck"
)

// TestMain fails the run if a queue goroutine outlives the tests (STR-9).
func TestMain(m *testing.M) { leakcheck.VerifyTestMain(m) }
