package forward

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/leakcheck"
)

// TestMain fails the run if a goroutine started for a call outlives the tests (STR-9).
func TestMain(m *testing.M) { leakcheck.VerifyTestMain(m) }
