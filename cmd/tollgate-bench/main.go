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
	fs.StringVar(&c.fixture, "fixture", "openai/chat-stream-text", "testkit fixture the fake upstream replays (see testkit.FixtureNames)")
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
	case testkit.Chunks(c.fixture) == nil || strings.Contains(c.fixture, "/request-"):
		return c, fmt.Errorf("unknown fixture %q (a response fixture from testkit.FixtureNames)", c.fixture)
	}
	return c, nil
}

// run measures the direct and proxied phases and writes the report to out.
func run(ctx context.Context, args []string, out io.Writer) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	r, err := newRig(ctx, cfg)
	if err != nil {
		return err
	}
	defer r.stop()
	// Burn-in: the first pass pays for cold code, caches and the Go runtime's
	// growth, which would otherwise make whichever phase runs first look slow.
	if _, err = measure(ctx, cfg, r.directTarget()); err != nil {
		return err
	}
	direct, err := measure(ctx, cfg, r.directTarget())
	if err != nil {
		return err
	}
	proxied, err := measure(ctx, cfg, r.proxyTarget())
	if err != nil {
		return err
	}
	drops, err := r.drops(ctx)
	if err != nil {
		return err
	}
	rep := buildReport(cfg, direct, proxied, drops)
	if cfg.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	return writeTable(out, rep)
}
