package forward

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

func TestReadBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		length  int64 // -1 unknown (chunked)
		want    string
		wantErr error
	}{
		{"under the limit", "abc", 3, "abc", nil},
		{"exactly at the limit", "abcd", 4, "abcd", nil},
		{"declared over the limit", "abcde", 5, "", ErrBodyTooLarge},
		{"chunked over the limit", "abcde", -1, "", ErrBodyTooLarge},
		{"empty", "", 0, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.ContentLength = tt.length
			got, err := ReadBody(httptest.NewRecorder(), r, 4)
			if !errors.Is(err, tt.wantErr) || string(got) != tt.want {
				t.Fatalf("got %q %v, want %q %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
	t.Run("read failure is 400", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", &chunkReader{err: errors.New("reset")})
		r.ContentLength = -1
		if _, err := ReadBody(httptest.NewRecorder(), r, 4); model.StatusOf(err, 0) != http.StatusBadRequest {
			t.Fatalf("err = %v", err)
		}
	})
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestForward_RoundTripFailures(t *testing.T) {
	timeout := rtFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	refused := rtFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("refused") })
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name       string
		ctx        context.Context
		rt         http.RoundTripper
		wantErr    error
		wantFinish model.Finish
	}{
		{"timeout is 504", context.Background(), timeout, ErrUpstreamTimeout, model.FinishTimeout},
		{"refused is 502", context.Background(), refused, ErrUpstreamUnreachable, model.FinishUpstreamError},
		{"client gone is silent", canceled, refused, nil, model.FinishClientCancel},
	}
	target, _ := url.Parse("http://127.0.0.1:1/v1")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Forward(tt.ctx, tt.rt, httptest.NewRecorder(), Request{Method: http.MethodPost, Target: target})
			if !errors.Is(err, tt.wantErr) || res.Finish != tt.wantFinish || res.Cut || res.Status != 0 {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
}

func TestForward_RelaysAndMarksCut(t *testing.T) {
	target, _ := url.Parse("http://upstream/v1")
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer k" || r.ContentLength != 2 {
			t.Errorf("request = %v len=%d", r.Header, r.ContentLength)
		}
		body := &chunkReader{chunks: []string{"data: 1\n\n"}, err: errors.New("reset")}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Keep-Alive": {"x"}}, Body: noopCloser{body}}, nil
	})
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Forward(ctx, rt, rec, Request{Method: http.MethodPost, Target: target, Header: http.Header{"Authorization": {"Bearer k"}}, Body: []byte("{}")})
	if err != nil || res.Status != 200 || res.Finish != model.FinishUpstreamError || !res.Cut {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if rec.Body.String() != "data: 1\n\n" || rec.Header().Get("Keep-Alive") != "" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("client got %q %v", rec.Body.String(), rec.Header())
	}
}

type noopCloser struct{ *chunkReader }

func (noopCloser) Close() error { return nil }

func TestFinishFor(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel2()
	bg := context.Background()
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want model.Finish
	}{
		{"client write", bg, ErrClientWrite, model.FinishClientCancel},
		{"client cancelled", canceled, errors.New("x"), model.FinishClientCancel},
		{"call deadline", expired, errors.New("x"), model.FinishTimeout},
		{"net timeout", bg, timeoutErr{}, model.FinishTimeout},
		{"upstream read", bg, ErrUpstreamRead, model.FinishUpstreamError},
	}
	for _, tt := range tests {
		if got := finishFor(tt.ctx, tt.err); got != tt.want {
			t.Errorf("%s: %s, want %s", tt.name, got, tt.want)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }
