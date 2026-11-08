package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

const exampleConfig = "../../configs/tollgated.example.yaml"

const chatBody = `{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}]}`

func writeConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tollgated.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func post(t *testing.T, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func waitExit(t *testing.T, done <-chan int, errb *syncBuffer) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("run did not return; stderr: %s", errb.String())
		return -1
	}
}

func TestServe_FromExampleConfigProxiesCall(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	t.Setenv("TOLLGATE_KEY_TEAM_A", "pk-team-a-secret")
	t.Setenv("TOLLGATE_KEY_TEAM_B", "pk-team-b-secret")
	t.Setenv("OPENAI_UPSTREAM", up.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout syncBuffer
	done, errb := startRun(ctx, []string{"serve", "-config", exampleConfig,
		"-listen", "127.0.0.1:0", "-admin-listen", "127.0.0.1:0"}, &stdout)
	waitFor(t, func() bool { return listenAddr(errb, "proxy") != "" && listenAddr(errb, "admin") != "" })
	base := "http://" + listenAddr(errb, "proxy")

	resp := post(t, base+"/openai/v1/chat/completions", chatBody,
		map[string]string{"x-proxy-key": "pk-team-a-secret", "Authorization": "Bearer sk-upstream-key"})
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	want, err := testkit.Fixture("openai/chat")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, want) {
		t.Fatalf("status %d, body %q; want 200 and the upstream fixture", resp.StatusCode, got)
	}
	reqs := up.Requests()
	if len(reqs) != 1 || string(reqs[0].Body) != chatBody {
		t.Fatalf("upstream saw %+v, want one request with the client body", reqs)
	}
	if reqs[0].Header.Get("Authorization") != "Bearer sk-upstream-key" || reqs[0].Header.Get("x-proxy-key") != "" {
		t.Errorf("upstream headers %v: want the provider key forwarded and the proxy key stripped", reqs[0].Header)
	}
	if r := post(t, base+"/openai/v1/chat/completions", chatBody, nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: status %d, want 401", r.StatusCode)
	}
	admin, err := http.Get("http://" + listenAddr(errb, "admin") + "/statusz")
	if err != nil || admin.StatusCode != http.StatusOK {
		t.Fatalf("statusz: %v %v", admin, err)
	}
	_ = admin.Body.Close()

	cancel()
	if code := waitExit(t, done, errb); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errb.String())
	}
	if !strings.Contains(stdout.String(), `"tenant":"team-a"`) {
		t.Errorf("metrics flushed to stdout = %q, want the team-a call", stdout.String())
	}
}

func TestServe_NeverLogsSecrets(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	t.Setenv("TOLLGATE_KEY_TEAM_A", "pk-team-a-secret")
	t.Setenv("TOLLGATE_KEY_TEAM_B", "pk-team-b-secret")
	t.Setenv("OPENAI_UPSTREAM", up.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done, errb := startRun(ctx, []string{"serve", "-config", exampleConfig, "-listen", "127.0.0.1:0", "-admin-listen", ""}, io.Discard)
	waitFor(t, func() bool { return listenAddr(errb, "proxy") != "" })
	cancel()
	waitExit(t, done, errb)
	log := errb.String()
	for _, secret := range []string{"pk-team-a-secret", "pk-team-b-secret"} {
		if strings.Contains(log, secret) {
			t.Errorf("startup log leaks %q:\n%s", secret, log)
		}
	}
	if !strings.Contains(log, "team-a: 1 key(s)") || !strings.Contains(log, "team-b: 1 key(s)") {
		t.Errorf("startup log lacks the redacted tenant summary:\n%s", log)
	}
}

func TestServe_InFlightStreamCompletesDuringShutdown(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: 100 * time.Millisecond})
	cfg := writeConfig(t, "providers:\n  openai: {upstream: \""+up.URL+"\"}\nmetrics: {sink: stdout}\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout syncBuffer
	done, errb := startRun(ctx, []string{"serve", "-config", cfg, "-listen", "127.0.0.1:0", "-shutdown-timeout", "10s"}, &stdout)
	waitFor(t, func() bool { return listenAddr(errb, "proxy") != "" })
	addr := listenAddr(errb, "proxy")

	resp := post(t, "http://"+addr+"/openai/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	first, err := br.ReadString('\n') // the stream is open and flowing
	if err != nil {
		t.Fatalf("first line: %v", err)
	}
	cancel() // SIGTERM equivalent, mid-stream
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("stream cut during shutdown: %v", err)
	}
	want, _ := testkit.Fixture("openai/chat-stream-text")
	if got := first + string(rest); got != string(want) {
		t.Fatalf("stream = %q, want the complete fixture", got)
	}
	if code := waitExit(t, done, errb); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errb.String())
	}
	if !strings.Contains(stdout.String(), `"stream":true`) {
		t.Errorf("metrics were not flushed on shutdown: %q", stdout.String())
	}
	if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
		t.Error("listener still accepts after shutdown")
	}
}

func TestServe_FlagsOverrideConfig(t *testing.T) {
	cfg := writeConfig(t, "listen: 127.0.0.1:1\nadmin_listen: 127.0.0.1:1\nshutdown_timeout: 1h\n")
	ctx, cancel := context.WithCancel(context.Background())
	done, errb := startRun(ctx, []string{"serve", "-config", cfg, "-listen", "127.0.0.1:0",
		"-admin-listen", "127.0.0.1:0", "-shutdown-timeout", "2s"}, io.Discard)
	waitFor(t, func() bool { return listenAddr(errb, "admin") != "" })
	cancel()
	if code := waitExit(t, done, errb); code != 0 {
		t.Fatalf("exit %d: flags did not override the config's unusable addresses; stderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), `"grace":"2s"`) {
		t.Errorf("shutdown timeout not overridden:\n%s", errb.String())
	}
}

func TestServe_InvalidConfigListsEveryProblem(t *testing.T) {
	cfg := writeConfig(t, `listen: 127.0.0.1:0
bogus_key: 1
shutdown_timeout: soon
auth:
  keys:
    team-a: ["${TG_TEST_UNSET_KEY}"]
metrics: {sink: postgres}
limits:
  connect_timeout: 5
  nope: 1
  hook_before_deadline: 2s
  hook_chain_deadline: 1s
`)
	var errb syncBuffer
	code := run(context.Background(), []string{"serve", "-config", cfg}, io.Discard, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	for _, want := range []string{"bogus_key", `shutdown_timeout: "soon"`, "TG_TEST_UNSET_KEY is not set",
		"postgres needs the ext/metrics/postgres module", "limits.connect_timeout: want a duration string",
		"limits.nope: unknown limit", "HookBeforeDeadline exceeds HookChainDeadline"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errb.String())
		}
	}
}
