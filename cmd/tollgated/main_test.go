package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRun_Commands(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"version", []string{"version"}, 0, buildVersion(), ""},
		{"help", []string{"help"}, 0, "usage:", ""},
		{"no args", nil, 2, "", "usage:"},
		{"unknown", []string{"bogus"}, 2, "", `unknown command "bogus"`},
		{"config without check", []string{"config"}, 2, "", "usage:"},
		{"config check without -config", []string{"config", "check"}, 2, "", "usage: tollgated config check"},
		{"missing config file", []string{"serve", "-config", "/nonexistent/x.yaml"}, 1, "", "invalid config"},
		{"bad flag", []string{"serve", "-nope"}, 2, "", "flag provided but not defined"},
		{"bad listen addr", []string{"serve", "-listen", "256.0.0.1:bad"}, 1, "", "listen failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(context.Background(), tc.args, &out, &errb)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stderr: %s)", code, tc.wantCode, errb.String())
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Fatalf("stdout = %q, want it to contain %q", out.String(), tc.wantOut)
			}
			if !strings.Contains(errb.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want it to contain %q", errb.String(), tc.wantErr)
			}
		})
	}
}

func TestRunServe_ShutsDownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var errb syncBuffer
	go func() {
		done <- run(ctx, []string{"serve", "-listen", "127.0.0.1:0", "-shutdown-timeout", "1s"}, io.Discard, &errb)
	}()
	waitFor(t, func() bool { return listenAddr(&errb, "proxy") != "" })
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code = %d, stderr: %s", code, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
}

func TestServe_ServesHealthThenDrains(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	errc := make(chan error, 1)
	go func() {
		errc <- serve(ctx, []endpoint{{"proxy", ln, h}}, time.Second, slog.New(slog.DiscardHandler))
	}()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServe_ReturnsListenerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close() // Serve fails immediately on a closed listener
	err = serve(context.Background(), []endpoint{{"proxy", ln, http.NotFoundHandler()}}, time.Second, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("want error from closed listener")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// listenAddr returns the address the named endpoint logged when it started
// listening, or "" if it has not yet.
func listenAddr(log *syncBuffer, name string) string {
	for _, l := range strings.Split(log.String(), "\n") {
		var rec struct{ Msg, Name, Addr string }
		if json.Unmarshal([]byte(l), &rec) == nil && rec.Msg == "tollgated listening" && rec.Name == name {
			return rec.Addr
		}
	}
	return ""
}

// startRun runs `tollgated args...` in the background and returns its exit
// code channel and stderr.
func startRun(ctx context.Context, args []string, stdout io.Writer) (<-chan int, *syncBuffer) {
	errb := &syncBuffer{}
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, stdout, errb) }()
	return done, errb
}
