package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
)

// checkResult is the machine-readable output of `config check -json`.
type checkResult struct {
	OK     bool     `json:"ok"`
	Errors []string `json:"errors"`
}

// runConfigCheck implements `tollgated config check -config FILE [-json]`: it
// builds the same Options and Proxy as serve, without listening. Exit codes:
// 0 valid, 1 invalid, 2 usage error.
func runConfigCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "YAML config file to validate (required)")
	asJSON := fs.Bool("json", false, "print {\"ok\":bool,\"errors\":[...]} on stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" || fs.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, "usage: tollgated config check -config FILE [-json]")
		return 2
	}
	pr, errs := prepare(*path, overrides{}, os.LookupEnv, io.Discard, slog.New(slog.DiscardHandler))
	if pr != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = pr.proxy.Close(ctx) // New started a session sweeper; nothing was served
	}
	res := checkResult{OK: len(errs) == 0, Errors: make([]string, 0, len(errs))}
	for _, err := range errs {
		res.Errors = append(res.Errors, err.Error())
	}
	writeCheck(res, *asJSON, stdout, stderr)
	if !res.OK {
		return 1
	}
	return 0
}

func writeCheck(res checkResult, asJSON bool, stdout, stderr io.Writer) {
	switch {
	case asJSON:
		_ = json.NewEncoder(stdout).Encode(res)
	case res.OK:
		_, _ = fmt.Fprintln(stdout, "config ok")
	default:
		_, _ = fmt.Fprintf(stderr, "tollgated: invalid config (%d problems)\n", len(res.Errors))
		for _, e := range res.Errors {
			_, _ = fmt.Fprintf(stderr, "  - %s\n", e)
		}
	}
}
