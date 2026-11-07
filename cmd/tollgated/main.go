// Command tollgated runs the tollgate library as a standalone proxy.
//
// It stays thin: parse flags, build tollgate.Options, serve, shut down
// gracefully. Logic belongs in the library or in ext/ (ARCHITECTURE.md).
//
//	tollgated serve [-listen :8080] [-shutdown-timeout 30s]
//	tollgated version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethosagent/tollgate"
)

// version is stamped at build time: -ldflags "-X main.version=v1.2.3".
var version = "0.0.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is main without process globals, so tests can drive it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	case "serve":
		return runServe(ctx, args[1:], stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: tollgated serve [-listen :8080] [-shutdown-timeout 30s] | tollgated version")
}

func runServe(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", ":8080", "address to listen on")
	grace := fs.Duration("shutdown-timeout", 30*time.Second, "time to drain in-flight calls on shutdown")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	p, err := tollgate.New(tollgate.Options{Logger: logger})
	if err != nil {
		logger.Error("invalid options", "err", err)
		return 1
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", *listen)
	if err != nil {
		logger.Error("listen failed", "addr", *listen, "err", err)
		return 1
	}
	if err := serve(ctx, ln, p.Handler(), *grace, logger); err != nil {
		logger.Error("server stopped", "err", err)
		return 1
	}
	return 0
}

// serve runs h on ln until ctx is done, then drains for up to grace.
func serve(ctx context.Context, ln net.Listener, h http.Handler, grace time.Duration, logger *slog.Logger) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	logger.Info("tollgated listening", "addr", ln.Addr().String(), "version", version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down", "grace", grace.String())
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
