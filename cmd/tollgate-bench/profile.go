package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
)

// profiles are the dev-only profile outputs of the latency pass. Each path,
// when set, receives one pprof file covering the timed pass only (warm-up
// and the memory pass excluded).
type profiles struct {
	cpu, mem, block, mutex string
}

// Block and mutex sampling rates while a profile is on: one blocking event
// per 10µs blocked, one in 5 contended mutex events. They are off otherwise.
const (
	blockRateNanos = 10_000
	mutexFraction  = 5
)

func (p *profiles) register(fs *flag.FlagSet) {
	fs.StringVar(&p.cpu, "cpuprofile", "", "dev: write a CPU profile of the timed pass to this file")
	fs.StringVar(&p.mem, "memprofile", "", "dev: write an allocation profile (allocs) after the timed pass to this file, and one from before it to FILE.before (pprof -diff_base)")
	fs.StringVar(&p.block, "blockprofile", "", "dev: write a block profile of the timed pass to this file")
	fs.StringVar(&p.mutex, "mutexprofile", "", "dev: write a mutex profile of the timed pass to this file")
}

// start turns on the requested profiles and returns the function that
// writes them; it must be called once the timed pass ends.
func (p profiles) start() (stop func() error, err error) {
	if p.mem != "" {
		runtime.GC() // the profile shows allocations as of the last GC
		if err := writeProfile("allocs", p.mem+".before"); err != nil {
			return nil, err
		}
	}
	var cpu *os.File
	if p.cpu != "" {
		if cpu, err = os.Create(p.cpu); err != nil {
			return nil, err
		}
		if err = pprof.StartCPUProfile(cpu); err != nil {
			return nil, errors.Join(err, cpu.Close())
		}
	}
	if p.block != "" {
		runtime.SetBlockProfileRate(blockRateNanos)
	}
	if p.mutex != "" {
		runtime.SetMutexProfileFraction(mutexFraction)
	}
	return func() error {
		var errs []error
		if p.mem != "" {
			runtime.GC()
		}
		if cpu != nil {
			pprof.StopCPUProfile()
			errs = append(errs, cpu.Close())
		}
		errs = append(errs, writeProfile("allocs", p.mem), writeProfile("block", p.block), writeProfile("mutex", p.mutex))
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
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := pprof.Lookup(name).WriteTo(f, 0); err != nil {
		return errors.Join(fmt.Errorf("%s profile: %w", name, err), f.Close())
	}
	return f.Close()
}
