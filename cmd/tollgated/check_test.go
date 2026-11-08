package main

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestConfigCheck(t *testing.T) {
	t.Setenv("TOLLGATE_KEY_TEAM_A", "ka")
	t.Setenv("TOLLGATE_KEY_TEAM_B", "kb")
	bad := writeConfig(t, "oops: 1\nmetrics: {sink: postgres}\n")
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOK   bool
		wantErrs int // minimum errors in -json output
	}{
		{"example ok", []string{"-config", exampleConfig, "-json"}, 0, true, 0},
		{"bad config", []string{"-config", bad, "-json"}, 1, false, 2},
		{"missing file", []string{"-config", "/nonexistent.yaml", "-json"}, 1, false, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(context.Background(), append([]string{"config", "check"}, tc.args...), &out, &errb)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stderr %s)", code, tc.wantCode, errb.String())
			}
			var res struct {
				OK     bool     `json:"ok"`
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("stdout %q is not JSON: %v", out.String(), err)
			}
			if res.OK != tc.wantOK || len(res.Errors) < tc.wantErrs || res.Errors == nil {
				t.Errorf("result = %+v, want ok=%v with >= %d errors and a non-null list", res, tc.wantOK, tc.wantErrs)
			}
		})
	}
}

func TestConfigCheck_PlainOutput(t *testing.T) {
	t.Setenv("TOLLGATE_KEY_TEAM_A", "ka")
	t.Setenv("TOLLGATE_KEY_TEAM_B", "kb")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"config", "check", "-config", exampleConfig}, &out, &errb); code != 0 || !strings.Contains(out.String(), "config ok") {
		t.Fatalf("code %d, out %q, err %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run(context.Background(), []string{"config", "check", "-config", writeConfig(t, "x: 1\n")}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "invalid config") {
		t.Fatalf("code %d, err %q", code, errb.String())
	}
	if code := run(context.Background(), []string{"config", "check", "-nope"}, &out, &errb); code != 2 {
		t.Fatalf("bad flag code = %d, want 2", code)
	}
}

func TestVersion_JSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"version", "-json"}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	var v struct{ Version, Go string }
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	if v.Version != version || v.Go != runtime.Version() {
		t.Errorf("version json = %+v, want %q / %q", v, version, runtime.Version())
	}
	if code := run(context.Background(), []string{"version", "-nope"}, &out, &errb); code != 2 {
		t.Errorf("bad flag code = %d, want 2", code)
	}
}
