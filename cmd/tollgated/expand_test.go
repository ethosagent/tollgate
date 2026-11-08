package main

import (
	"strings"
	"testing"
)

func TestExpand_RejectsUnbracedReferences(t *testing.T) {
	env := envOf(map[string]string{"VAR": "secret"})
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"unbraced", "$VAR", "", true},
		{"underscore start", "$_X", "", true},
		{"prefixed", "pre$VAR", "", true},
		{"braced set", "${VAR}", "secret", false},
		{"digit after dollar", "price$5", "price$5", false},
		{"trailing dollar", "abc$", "abc$", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c collector
			got := c.expand("auth.keys.team-a[0]", tt.in, env)
			if tt.wantErr {
				msg := joined(c.errs)
				if len(c.errs) != 1 || !strings.Contains(msg, "auth.keys.team-a[0]") || !strings.Contains(msg, "use ${NAME}") {
					t.Fatalf("errs = %q", msg)
				}
				if strings.Contains(msg, tt.in) {
					t.Fatalf("error leaks the value: %q", msg)
				}
				return
			}
			if len(c.errs) > 0 || got != tt.want {
				t.Fatalf("got %q errs %q, want %q", got, joined(c.errs), tt.want)
			}
		})
	}
}

func TestExpand_UnbracedKeyIsConfigError(t *testing.T) {
	_, errs := prep(t, "auth:\n  keys:\n    team-a: ['$TOLLGATE_KEY']\n", nil)
	if got := joined(errs); !strings.Contains(got, "auth.keys.team-a[0]") || !strings.Contains(got, "not expanded") {
		t.Fatalf("errs = %q", got)
	}
}

func TestDecodeConfig_SingleDocumentOnly(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"one document", "listen: ':1'\n", false},
		{"leading marker", "---\nlisten: ':1'\n", false},
		{"empty", "", false},
		{"second valid document", "listen: ':1'\n---\nlisten: ':2'\n", true},
		{"second document unknown key", "listen: ':1'\n---\nbogus: 1\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs := decodeConfig(strings.NewReader(tt.in))
			if tt.wantErr != (len(errs) > 0) {
				t.Fatalf("errs = %q, wantErr %v", joined(errs), tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(joined(errs), "more than one YAML document") {
				t.Fatalf("errs = %q", joined(errs))
			}
		})
	}
}

func TestRun_RejectsPositionalArgs(t *testing.T) {
	for _, args := range [][]string{{"serve", "extra"}, {"version", "extra"}, {"version", "-json", "extra"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errb strings.Builder
			if code := run(t.Context(), args, &out, &errb); code != 2 {
				t.Fatalf("code = %d, want 2", code)
			}
			if !strings.Contains(errb.String(), "usage: tollgated "+args[0]) || out.Len() != 0 {
				t.Fatalf("stderr %q stdout %q", errb.String(), out.String())
			}
		})
	}
}
