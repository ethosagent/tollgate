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
	"sync"
	"time"
)

// endpoint is one listener and the handler it serves.
type endpoint struct {
	name string
	ln   net.Listener
	h    http.Handler
}

// runServe implements `tollgated serve`. Exit codes: 0 clean stop, 1 invalid
// config or runtime failure, 2 usage error.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "YAML config file (default: pass-through defaults)")
	var ov overrides
	fs.StringVar(&ov.listen, "listen", defaultListen, "address to listen on (overrides the config file)")
	fs.StringVar(&ov.adminListen, "admin-listen", "", "address for /statusz and /metrics (overrides the config file)")
	fs.DurationVar(&ov.shutdown, "shutdown-timeout", defaultShutdown, "time to drain in-flight calls on shutdown (overrides the config file)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			ov.setListen = true
		case "admin-listen":
			ov.setAdmin = true
		case "shutdown-timeout":
			ov.setShutdown = true
		}
	})
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	pr, errs := prepare(*path, ov, os.LookupEnv, stdout, logger)
	if len(errs) > 0 {
		reportErrors(stderr, errs)
		return 1
	}
	return serveProxy(ctx, pr, *path, logger)
}

// reportErrors prints every config problem, one per line.
func reportErrors(w io.Writer, errs []error) {
	_, _ = fmt.Fprintf(w, "tollgated: invalid config (%d problems)\n", len(errs))
	for _, err := range errs {
		_, _ = fmt.Fprintf(w, "  - %v\n", err)
	}
}

// serveProxy starts the proxy, serves it until ctx is done, then drains and
// flushes metrics. The startup log carries the redacted summary only.
func serveProxy(ctx context.Context, pr *prepared, path string, logger *slog.Logger) int {
	logger.Info("config loaded", "config", path, "tenants", pr.summary.Tenants, "providers", pr.summary.Providers,
		"admission", pr.summary.Admission, "metrics", pr.summary.Metrics)
	if err := pr.proxy.Start(ctx); err != nil {
		logger.Error("start failed", "err", err)
		return 1
	}
	code := 0
	if err := listenAndServe(ctx, pr, logger); err != nil {
		logger.Error("server stopped", "err", err)
		code = 1
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pr.shutdown)
	defer cancel()
	if err := pr.proxy.Close(closeCtx); err != nil {
		logger.Error("close failed", "err", err)
		code = 1
	}
	return code
}

func listenAndServe(ctx context.Context, pr *prepared, logger *slog.Logger) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", pr.listen)
	if err != nil {
		logger.Error("listen failed", "addr", pr.listen, "err", err)
		return errors.New("listen failed")
	}
	eps := []endpoint{{"proxy", ln, pr.proxy.Handler()}}
	if pr.adminListen != "" {
		aln, err := lc.Listen(ctx, "tcp", pr.adminListen)
		if err != nil {
			_ = ln.Close()
			logger.Error("listen failed", "addr", pr.adminListen, "err", err)
			return errors.New("listen failed")
		}
		eps = append(eps, endpoint{"admin", aln, pr.proxy.AdminHandler()})
	}
	return serve(ctx, eps, pr.shutdown, logger)
}

// serve runs every endpoint until ctx is done (or one fails), then stops
// accepting on all of them at once and drains in-flight calls for up to grace.
// Calls still running after grace are cut.
func serve(ctx context.Context, eps []endpoint, grace time.Duration, logger *slog.Logger) error {
	srvs := make([]*http.Server, len(eps))
	errc := make(chan error, len(eps))
	for i, ep := range eps {
		srvs[i] = &http.Server{
			Handler:           ep.h,
			ReadHeaderTimeout: 10 * time.Second,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		}
		go func() { errc <- srvs[i].Serve(ep.ln) }()
		logger.Info("tollgated listening", "name", ep.name, "addr", ep.ln.Addr().String(), "version", version)
	}
	pending := len(eps)
	var firstErr error
	select {
	case firstErr = <-errc:
		pending--
	case <-ctx.Done():
	}
	logger.Info("shutting down", "grace", grace.String())
	err := shutdownAll(context.WithoutCancel(ctx), srvs, grace)
	for ; pending > 0; pending-- {
		if e := <-errc; e != nil && !errors.Is(e, http.ErrServerClosed) {
			firstErr = errors.Join(firstErr, e)
		}
	}
	return errors.Join(firstErr, err)
}

// shutdownAll shuts every server down concurrently under one grace deadline,
// so no listener keeps accepting while another drains. A server that is still
// busy at the deadline is closed.
func shutdownAll(ctx context.Context, srvs []*http.Server, grace time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, grace)
	defer cancel()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, s := range srvs {
		wg.Go(func() {
			if err := s.Shutdown(ctx); err != nil {
				_ = s.Close()
				mu.Lock()
				errs = append(errs, fmt.Errorf("shutdown: %w", err))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
