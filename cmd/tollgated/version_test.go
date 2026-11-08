package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionFrom(t *testing.T) {
	mod := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	tests := []struct {
		name  string
		stamp string
		info  *debug.BuildInfo
		ok    bool
		want  string
	}{
		{"release build keeps the ldflags stamp", "v0.2.0", mod("v0.1.0"), true, "v0.2.0"},
		{"go install @version reports the module version", devVersion, mod("v0.1.0"), true, "v0.1.0"},
		{"checkout build stays dev", devVersion, mod("(devel)"), true, devVersion},
		{"empty module version stays dev", devVersion, mod(""), true, devVersion},
		{"no build info stays dev", devVersion, nil, false, devVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionFrom(tt.stamp, tt.info, tt.ok); got != tt.want {
				t.Fatalf("versionFrom(%q) = %q, want %q", tt.stamp, got, tt.want)
			}
		})
	}
}
