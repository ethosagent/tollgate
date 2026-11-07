package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRun_WritesProfilesAndGoroutines: -profile leaves every pprof file,
// non-empty, and the memory pass counts goroutines per stream.
func TestRun_WritesProfilesAndGoroutines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "prof")
	var out bytes.Buffer
	args := []string{"-sessions", "2", "-calls", "1", "-chunk-delay", "1ms", "-json", "-profile", dir}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"cpu", "allocs", "allocs.before", "block", "mutex", "heap"} {
		if fi, err := os.Stat(filepath.Join(dir, n+".pprof")); err != nil || fi.Size() == 0 {
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

func TestProfiles_BadDirFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (profiles{dir: filepath.Join(file, "prof")}).start(); err == nil {
		t.Fatal("profiling started under a regular file")
	}
	stop, err := (profiles{}).start()
	if err != nil || stop() != nil {
		t.Fatalf("profiling off: start err %v", err)
	}
}

// TestRun_NullProxyControl: the control relay serves every proxied call
// byte for byte (the loader checks the body) and the report says it ran.
func TestRun_NullProxyControl(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-sessions", "2", "-calls", "2", "-chunk-delay", "1ms", "-json", "-null-proxy"}, &out); err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !r.NullProxy || r.Overhead.Pairs != 4 || r.Memory.ProxyStreams != 2 {
		t.Fatalf("report = %+v", r)
	}
}
