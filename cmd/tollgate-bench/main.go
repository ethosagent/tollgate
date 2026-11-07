package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// config is the parsed command line.
type config struct {
	sessions   int
	calls      int
	fixture    string
	chunkDelay time.Duration
	json       bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tollgate-bench:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (config, error) {
	var c config
	fs := flag.NewFlagSet("tollgate-bench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&c.sessions, "sessions", 50, "concurrent sessions")
	fs.IntVar(&c.calls, "calls", 4, "sequential calls per session")
	fs.StringVar(&c.fixture, "fixture", "openai/chat-stream-text", "testkit response fixture the fake upstream replays (see testkit.FixtureNames); must answer 200")
	fs.DurationVar(&c.chunkDelay, "chunk-delay", 0, "fake upstream delay between stream chunks; holds streams open")
	fs.BoolVar(&c.json, "json", false, "print one JSON document instead of a table")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	switch {
	case c.sessions < 1:
		return c, errors.New("-sessions must be at least 1")
	case c.calls < 1:
		return c, errors.New("-calls must be at least 1")
	case c.chunkDelay < 0:
		return c, errors.New("-chunk-delay must not be negative")
	case testkit.Chunks(c.fixture) == nil || strings.Contains(c.fixture, "/request-") || strings.Contains(c.fixture, "/error-"):
		return c, fmt.Errorf("unknown fixture %q (a 200 response fixture from testkit.FixtureNames)", c.fixture)
	}
	return c, nil
}

// run measures latency (paired, on one rig), then memory (on its own rig),
// and writes the report to out.
func run(ctx context.Context, args []string, out io.Writer) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	rep, err := measure(ctx, cfg)
	if err != nil {
		return err
	}
	if rep.Memory, err = memoryPass(ctx, cfg); err != nil {
		return err
	}
	if cfg.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	return writeTable(out, rep)
}

// measure runs one warm-up pair per session (both paths, discarded), then
// -calls measured pairs per session, and reads the drop counters.
func measure(ctx context.Context, cfg config) (report, error) {
	r, err := newRig(ctx, cfg.sessions, cfg.chunkDelay)
	if err != nil {
		return report{}, err
	}
	defer r.stop()
	l := newLoader(cfg.fixture, cfg.sessions)
	defer l.close()
	if _, err := l.pairs(ctx, r, cfg.sessions, 1, "warm"); err != nil {
		return report{}, err
	}
	runtime.GC() // start the timed pass from a clean heap; nothing forces a GC inside it
	pairs, err := l.pairs(ctx, r, cfg.sessions, cfg.calls, "run")
	if err != nil {
		return report{}, err
	}
	t, err := analyse(pairs, r.upstreamSide(), r.proxyArrival)
	if err != nil {
		return report{}, err
	}
	drops, err := r.drops(ctx)
	if err != nil {
		return report{}, err
	}
	return report{
		Fixture: cfg.fixture, Sessions: cfg.sessions, CallsPerSession: cfg.calls,
		TotalCalls: len(pairs), WarmupCalls: cfg.sessions, ChunkDelay: cfg.chunkDelay.String(),
		Direct: latencyOf(t.directTTFB, t.directTotal), Proxy: latencyOf(t.proxyTTFB, t.proxyTotal),
		Overhead: overheadOf(t), PeakStreams: l.peak.Load(), Statusz: drops,
	}, nil
}
