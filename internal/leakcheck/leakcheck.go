package leakcheck

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Module is the import path prefix whose goroutines VerifyTestMain polices.
const Module = "github.com/ethosagent/tollgate"

// settle is how long VerifyTestMain waits for goroutines to finish.
const settle = 5 * time.Second

// VerifyTestMain runs the tests and, if they passed, panics when a goroutine
// from this module is still running (pooled client connections are not
// ours: their stacks name only net/http). Call it from TestMain:
//
//	func TestMain(m *testing.M) { leakcheck.VerifyTestMain(m) }
func VerifyTestMain(m *testing.M) {
	code := m.Run()
	if code != 0 {
		return // the test binary exits with code; a leak report would only add noise
	}
	if err := Find(Module, settle); err != nil {
		panic(err)
	}
}

// Find returns an error describing every goroutine, other than the caller,
// whose stack mentions prefix. It polls until wait has passed, so goroutines
// that are about to exit are given time to.
func Find(prefix string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		leaks := scan(prefix)
		if len(leaks) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("leakcheck: %d goroutine(s) outlived the tests:\n\n%s", len(leaks), strings.Join(leaks, "\n\n"))
		}
		time.Sleep(10 * time.Millisecond)
		runtime.Gosched()
	}
}

func scan(prefix string) []string {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var leaks []string
	for i, g := range strings.Split(string(buf), "\n\n") {
		if i == 0 { // the calling goroutine
			continue
		}
		if strings.Contains(g, prefix) {
			leaks = append(leaks, g)
		}
	}
	return leaks
}
