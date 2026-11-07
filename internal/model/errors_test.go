package model

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

type statusErr int

func (e statusErr) Error() string   { return "status" }
func (e statusErr) HTTPStatus() int { return int(e) }

type retryErr time.Duration

func (e retryErr) Error() string             { return "retry" }
func (e retryErr) RetryAfter() time.Duration { return time.Duration(e) }

func TestStatusOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil keeps fallback", nil, http.StatusUnauthorized},
		{"plain error keeps fallback", errors.New("x"), http.StatusUnauthorized},
		{"4xx is honoured", statusErr(http.StatusForbidden), http.StatusForbidden},
		{"5xx is honoured", statusErr(http.StatusServiceUnavailable), http.StatusServiceUnavailable},
		{"wrapped is found", fmt.Errorf("ctx: %w", statusErr(http.StatusTooManyRequests)), http.StatusTooManyRequests},
		{"2xx is ignored", statusErr(http.StatusOK), http.StatusUnauthorized},
		{"out of range is ignored", statusErr(600), http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusOf(tt.err, http.StatusUnauthorized); got != tt.want {
				t.Fatalf("StatusOf = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRetryAfterOf(t *testing.T) {
	if d, ok := RetryAfterOf(fmt.Errorf("w: %w", retryErr(2*time.Second))); !ok || d != 2*time.Second {
		t.Fatalf("got %v %v", d, ok)
	}
	if _, ok := RetryAfterOf(errors.New("x")); ok {
		t.Fatal("plain error reported a retry delay")
	}
}
