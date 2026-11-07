package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRun_WritesProfilesAndGoroutines: every -*profile flag leaves a
// non-empty pprof file, and the memory pass counts goroutines per stream.
func TestRun_WritesProfilesAndGoroutines(t *testing.T) {
	dir := t.TempDir()
	path := func(n string) string { return filepath.Join(dir, n) }
	var out bytes.Buffer
	args := []string{"-sessions", "2", "-calls", "1", "-chunk-delay", "1ms", "-json",
		"-cpuprofile", path("cpu"), "-memprofile", path("mem"), "-blockprofile", path("block"), "-mutexprofile", path("mutex")}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"cpu", "mem", "mem.before", "block", "mutex"} {
		if fi, err := os.Stat(path(n)); err != nil || fi.Size() == 0 {
			t.Errorf("profile %s: %v", n, err)
		}
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	// Proxied, at least the proxy's server connection and its upstream
	// connection run goroutines that the direct path does not have.
	if m := r.Memory; m.ProxyGoroutinesPerStream <= m.DirectGoroutinesPerStream || m.AddedGoroutinesPerStream <= 0 {
		t.Fatalf("goroutines per stream = %+v", m)
	}
}

func TestProfiles_BadPathFails(t *testing.T) {
	p := profiles{cpu: filepath.Join(t.TempDir(), "missing", "cpu")}
	if _, err := p.start(); err == nil {
		t.Fatal("a CPU profile path in a missing directory started")
	}
}
