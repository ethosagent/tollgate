// Command tollgated runs the tollgate library as a standalone proxy.
//
// It stays thin: parse flags, build tollgate.Options, serve, shut down
// gracefully. Logic belongs in the library or in ext/ (ARCHITECTURE.md).
//
//	tollgated serve [-config FILE] [-listen ADDR] [-admin-listen ADDR] [-shutdown-timeout D]
//	tollgated config check -config FILE [-json]
//	tollgated version [-json]
//
// The config file is YAML (configs/tollgated.example.yaml); flags override it.
// Secrets come from ${VAR} in the file and are never logged. Exit codes: 0 ok,
// 1 invalid config or runtime failure, 2 usage error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
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
		return runVersion(args[1:], stdout, stderr)
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "config":
		if len(args) > 1 && args[1] == "check" {
			return runConfigCheck(ctx, args[2:], stdout, stderr)
		}
		usage(stderr)
		return 2
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
	_, _ = fmt.Fprintln(w, "usage: tollgated serve [-config FILE] [-listen ADDR] [-admin-listen ADDR] [-shutdown-timeout D]")
	_, _ = fmt.Fprintln(w, "       tollgated config check -config FILE [-json]")
	_, _ = fmt.Fprintln(w, "       tollgated version [-json]")
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print {\"version\":...,\"go\":...} on stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*asJSON {
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	}
	_ = json.NewEncoder(stdout).Encode(struct {
		Version string `json:"version"`
		Go      string `json:"go"`
	}{version, runtime.Version()})
	return 0
}
