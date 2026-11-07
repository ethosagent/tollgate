package session

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/leakcheck"
)

// TestMain fails the run if a goroutine started by a Strategy outlives the tests.
func TestMain(m *testing.M) { leakcheck.VerifyTestMain(m) }
