package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
)

// profiles are the dev-only pprof outputs, all written to one directory
// when -profile is set: cpu, allocs (with allocs.before, for pprof
// -diff_base), block and mutex cover the timed pass only (warm-up and the
// memory pass excluded); heap is the live heap while the memory pass holds
// every proxied stream open.
type profiles struct {
	dir string
}

// Block and mutex sampling rates while profiling: one blocking event per
// 10µs blocked, one in 5 contended mutex events. They are off otherwise.
const (
	blockRateNanos = 10_000
	mutexFraction  = 5
)

func (p *profiles) register(fs *flag.FlagSet) {
	fs.StringVar(&p.dir, "profile", "", "dev: write pprof files to this directory (created if missing): cpu, allocs, allocs.before, block, mutex for the timed pass, heap at peak open streams")
}

// path is where the named profile goes; "" when profiling is off.
func (p profiles) path(name string) string {
	if p.dir == "" {
		return ""
	}
	return filepath.Join(p.dir, name+".pprof")
}

// start turns profiling on and returns the function that writes the
// timed-pass profiles; it must be called once the timed pass ends.
func (p profiles) start() (stop func() error, err error) {
	if p.dir == "" {
		return func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Clean(p.dir), 0o750); err != nil {
		return nil, err
	}
	runtime.GC() // the allocs profile shows allocations as of the last GC
	if err := writeProfile("allocs", p.path("allocs.before")); err != nil {
		return nil, err
	}
	cpu, err := os.Create(filepath.Clean(p.path("cpu")))
	if err != nil {
		return nil, err
	}
	if err = pprof.StartCPUProfile(cpu); err != nil {
		return nil, errors.Join(err, cpu.Close())
	}
	runtime.SetBlockProfileRate(blockRateNanos)
	runtime.SetMutexProfileFraction(mutexFraction)
	return func() error {
		runtime.GC()
		pprof.StopCPUProfile()
		errs := []error{cpu.Close(), writeProfile("allocs", p.path("allocs")),
			writeProfile("block", p.path("block")), writeProfile("mutex", p.path("mutex"))}
		runtime.SetBlockProfileRate(0)
		runtime.SetMutexProfileFraction(0)
		return errors.Join(errs...)
	}, nil
}

// writeProfile writes the named runtime profile to path; "" writes nothing.
func writeProfile(name, path string) error {
	if path == "" {
		return nil
	}
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		return err
	}
	if err := pprof.Lookup(name).WriteTo(f, 0); err != nil {
		return errors.Join(fmt.Errorf("%s profile: %w", name, err), f.Close())
	}
	return f.Close()
}
