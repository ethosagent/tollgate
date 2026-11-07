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
	prof       profiles
	// upstream is the address line of a fake in another process
	// (-serve-fake); empty runs the fake in this process.
	upstream  string
	serveFake bool // this process is only that fake
	nullProxy bool // control: a minimal stdlib relay replaces tollgate on the proxied path
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
	c.prof.register(fs)
	fs.StringVar(&c.upstream, "upstream", "", "use the fake of another process: the line a -serve-fake process prints (separates its runtime from the proxy's)")
	fs.BoolVar(&c.nullProxy, "null-proxy", false, "control: measure a minimal stdlib relay in tollgate's place (what any proxy hop costs on this rig)")
	fs.BoolVar(&c.serveFake, "serve-fake", false, "serve only the fake upstream (with -chunk-delay) and print its -upstream line; Ctrl-C ends it")
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
	if cfg.serveFake {
		return serveFake(ctx, cfg.chunkDelay, out)
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
	r, err := newRig(ctx, cfg, cfg.chunkDelay)
	if err != nil {
		return report{}, err
	}
	defer r.stop()
	l := newLoader(cfg.fixture, cfg.sessions)
	defer l.close()
	if _, err := l.pairs(ctx, r, cfg.sessions, 1, "warm"); err != nil {
		return report{}, err
	}
	pairs, err := timedPass(ctx, cfg, l, r)
	if err != nil {
		return report{}, err
	}
	up, bound, err := r.upstreamSide(ctx)
	if err != nil {
		return report{}, err
	}
	t, err := analyse(pairs, up, r.proxyArrival)
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
		UpstreamProc: cfg.upstream != "", NullProxy: cfg.nullProxy, GOMAXPROCS: runtime.GOMAXPROCS(0),
		ClockBoundMs: ms(bound),
	}, nil
}

// timedPass runs the measured pairs, under the -*profile flags if set.
func timedPass(ctx context.Context, cfg config, l *loader, r *rig) ([]pair, error) {
	runtime.GC() // start the timed pass from a clean heap; nothing forces a GC inside it
	stopProf, err := cfg.prof.start()
	if err != nil {
		return nil, err
	}
	pairs, err := l.pairs(ctx, r, cfg.sessions, cfg.calls, "run")
	if perr := stopProf(); err == nil && perr != nil {
		err = fmt.Errorf("profiles: %w", perr)
	}
	return pairs, err
}
