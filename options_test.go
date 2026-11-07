package tollgate

import (
	"strings"
	"testing"
	"time"
)

// The plan's documented defaults. A change here is a behaviour change for
// every host: update ARCHITECTURE.md and the changelog with it.
func TestDefaultLimits_MatchDocumentedValues(t *testing.T) {
	d := DefaultLimits()
	checks := []struct {
		name      string
		got, want any
	}{
		{"MaxRequestBody", d.MaxRequestBody, int64(16 << 20)},
		{"ConnectTimeout", d.ConnectTimeout, 10 * time.Second},
		{"FirstByteTimeout", d.FirstByteTimeout, 60 * time.Second},
		{"IdleStreamTimeout", d.IdleStreamTimeout, 120 * time.Second},
		{"MaxCallDuration", d.MaxCallDuration, 15 * time.Minute},
		{"HookBeforeDeadline", d.HookBeforeDeadline, 50 * time.Millisecond},
		{"HookChainDeadline", d.HookChainDeadline, 200 * time.Millisecond},
		{"ExtensionDeadline", d.ExtensionDeadline, 5 * time.Second},
		{"AdmissionDeadline", d.AdmissionDeadline, 30 * time.Second},
		{"FinalizerBudget", d.FinalizerBudget, 2},
		{"RecoveryCeiling", d.RecoveryCeiling, 10},
		{"EventBuffer", d.EventBuffer, 1024},
		{"MetricQueue", d.MetricQueue, 10_000},
		{"MetricBatch", d.MetricBatch, 500},
		{"MetricFlushInterval", d.MetricFlushInterval, time.Second},
		{"MetricWriteTimeout", d.MetricWriteTimeout, 10 * time.Second},
		{"MetricFailAfter", d.MetricFailAfter, 3},
		{"SessionTTL", d.SessionTTL, 5 * time.Minute},
		{"MaxSessions", d.MaxSessions, 100_000},
		{"MaxConnsPerHost", d.MaxConnsPerHost, 100},
		{"MaxIdleConnsPerHost", d.MaxIdleConnsPerHost, 20},
		{"IdleConnTimeout", d.IdleConnTimeout, 30 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLimits_PartialOverrideKeepsOtherDefaults(t *testing.T) {
	got := Limits{EventBuffer: 8, SessionTTL: time.Minute}.withDefaults()
	want := DefaultLimits()
	want.EventBuffer = 8
	want.SessionTTL = time.Minute
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestHeaderNames_PartialOverride(t *testing.T) {
	got := HeaderNames{TenantKey: "x-api-token"}.withDefaults()
	if got.TenantKey != "x-api-token" || got.Session != "x-session-id" || got.UpstreamURL != "x-upstream-url" {
		t.Fatalf("got %+v", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr string // empty means valid
	}{
		{"zero value is valid", Options{}, ""},
		{"negative body cap", Options{Limits: Limits{MaxRequestBody: -1}}, "MaxRequestBody must not be negative"},
		{"negative duration", Options{Limits: Limits{IdleStreamTimeout: -time.Second}}, "IdleStreamTimeout"},
		{"negative extension deadline", Options{Limits: Limits{ExtensionDeadline: -time.Second}}, "ExtensionDeadline must not be negative"},
		{"negative admission deadline", Options{Limits: Limits{AdmissionDeadline: -time.Second}}, "AdmissionDeadline must not be negative"},
		{"negative metric batch", Options{Limits: Limits{MetricBatch: -1}}, "MetricBatch must not be negative"},
		{"negative metric flush interval", Options{Limits: Limits{MetricFlushInterval: -time.Second}}, "MetricFlushInterval must not be negative"},
		{"per-hook deadline above chain", Options{Limits: Limits{HookBeforeDeadline: time.Second}}, "HookBeforeDeadline exceeds"},
		{"finalizer budget above ceiling", Options{Limits: Limits{FinalizerBudget: 11}}, "FinalizerBudget exceeds"},
		{"duplicate header names", Options{Headers: HeaderNames{TenantKey: "X-Session-Id"}}, "both"},
		{"reserved credential header", Options{Headers: HeaderNames{TenantKey: "Authorization"}}, "reserved"},
		{"upstream key header reserved", Options{Headers: HeaderNames{Session: "x-api-key"}}, "reserved"},
		{"nil finalizer", Options{Finalizers: []Finalizer{nil}}, "finalizers[0] is nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.opts.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_ReportsAllProblemsAtOnce(t *testing.T) {
	err := Options{Limits: Limits{MaxRequestBody: -1, EventBuffer: -1}}.Validate()
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"MaxRequestBody", "EventBuffer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

type claimer struct {
	NoAuth
	names []string
}

func (c claimer) ConsumedHeaders() []string { return c.names }

func TestValidate_ConsumedHeaders(t *testing.T) {
	tests := []struct {
		name    string
		claims  []string
		wantErr bool
	}{
		{"custom credential header is fine", []string{"x-team-key"}, false},
		{"authorization cannot be claimed", []string{"Authorization"}, true},
		{"x-api-key cannot be claimed", []string{"X-Api-Key"}, true},
		{"empty name is rejected", []string{" "}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(Options{Auth: claimer{names: tt.claims}})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
