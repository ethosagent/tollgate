package model

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestNoAuth_DefaultAndExplicitTenant(t *testing.T) {
	r := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	tests := []struct {
		name string
		auth NoAuth
		want string
	}{
		{"empty uses default", NoAuth{}, DefaultTenantID},
		{"explicit id", NoAuth{TenantID: "team-a"}, "team-a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.auth.Authenticate(context.Background(), r)
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			if got.ID != tc.want {
				t.Fatalf("tenant = %q, want %q", got.ID, tc.want)
			}
		})
	}
}

func TestNoopHook_PassesThrough(t *testing.T) {
	ctx := context.Background()
	h := NoopHook{}
	c := &Call{ID: "c1"}
	if resp, err := h.Before(ctx, c); resp != nil || err != nil {
		t.Fatalf("Before = (%v, %v), want (nil, nil)", resp, err)
	}
	in := &Response{Status: 200}
	out, err := h.OnResponse(ctx, c, in)
	if err != nil || out != in {
		t.Fatalf("OnResponse must return its input unchanged, got (%v, %v)", out, err)
	}
	h.OnEvent(ctx, c, Event{Kind: EventTextDelta})
	h.After(ctx, c, Metric{})
	h.OnSessionClosed(ctx, Session{})
	if h.Name() != "noop" {
		t.Fatalf("Name = %q", h.Name())
	}
}

func TestUnlimited_ReleaseIsSafe(t *testing.T) {
	release, err := Unlimited{}.Acquire(context.Background(), Tenant{}, &Call{})
	if err != nil || release == nil {
		t.Fatalf("Acquire: release nil=%t, err=%v", release == nil, err)
	}
	release()
}

func TestDiscardSink_NeverFails(t *testing.T) {
	if err := (DiscardSink{}).Write(context.Background(), []Metric{{CallID: "x"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
}
