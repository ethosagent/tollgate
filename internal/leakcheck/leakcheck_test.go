package leakcheck

import (
	"strings"
	"testing"
	"time"
)

const self = "internal/leakcheck"

func TestFind_ReportsAGoroutineUntilItEnds(t *testing.T) {
	stop := make(chan struct{})
	go func() { <-stop }()
	err := Find(self, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "leakcheck_test.go") {
		t.Fatalf("Find = %v, want a leak naming this file", err)
	}
	close(stop)
	if err := Find(self, 2*time.Second); err != nil {
		t.Fatalf("Find after the goroutine ended = %v", err)
	}
}

func TestFind_IgnoresOtherPackages(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	go func() { <-stop }()
	if err := Find("no/such/module/path", 50*time.Millisecond); err != nil {
		t.Fatalf("Find = %v, want nil for an unrelated prefix", err)
	}
}
