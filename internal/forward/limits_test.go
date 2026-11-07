package forward

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// script drives the upstream body from a goroutine. ctx ends when the call
// does. It returns when it is done; the harness closes the pipe.
type script func(ctx context.Context, pw *io.PipeWriter)

func send(pw *io.PipeWriter, s string) { _, _ = pw.Write([]byte(s)) }

// stall blocks until the call ends, like an upstream that went quiet.
func stall(ctx context.Context, _ *io.PipeWriter) { <-ctx.Done() }

type limitCase struct {
	name        string
	limits      StreamLimits
	shape       normalise.ErrorShape
	contentType string // default text/event-stream
	headerDelay time.Duration
	body        script
	wantErr     error
	wantFinish  model.Finish
	wantCut     bool
	wantBody    string // exact prefix of the client body ("" with wantEmpty)
	wantEvent   string // substring of the appended error event
	wantAfter   time.Duration
}

const (
	frameA   = "data: a\n\n"
	typeJSON = "application/json"
)

// runScript drives the upstream body, then ends the pipe with the call's error.
func runScript(ctx context.Context, body script, pw *io.PipeWriter, done chan<- struct{}) {
	body(ctx, pw)
	pw.CloseWithError(ctx.Err())
	done <- struct{}{}
}

func runLimitCase(t *testing.T, tt limitCase) {
	synctest.Test(t, func(t *testing.T) {
		scripts := make(chan struct{}, 1)
		started := false
		rt := rtFunc(func(r *http.Request) (*http.Response, error) {
			select {
			case <-time.After(tt.headerDelay):
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			pr, pw := io.Pipe()
			started = true
			go runScript(r.Context(), tt.body, pw, scripts)
			ct := tt.contentType
			if ct == "" {
				ct = "text/event-stream"
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: pr}, nil
		})
		target, _ := url.Parse("http://upstream/v1")
		rec := httptest.NewRecorder()
		start := time.Now()
		res, err := Forward(context.Background(), rt, rec, Request{Method: "POST", Target: target, Limits: tt.limits, Shape: tt.shape})
		if !errors.Is(err, tt.wantErr) || res.Finish != tt.wantFinish || res.Cut != tt.wantCut {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if got := time.Since(start); tt.wantAfter != 0 && got != tt.wantAfter {
			t.Errorf("ended after %v of fake time, want %v", got, tt.wantAfter)
		}
		if started {
			<-scripts // the upstream side ends with the call
		}
		got := rec.Body.String()
		if !strings.HasPrefix(got, tt.wantBody) || (tt.wantBody == "" && got != "") {
			t.Errorf("client body = %q, want prefix %q", got, tt.wantBody)
		}
		if tt.wantEvent != "" && !strings.Contains(got, tt.wantEvent) {
			t.Errorf("client body = %q, want an event containing %q", got, tt.wantEvent)
		}
		if tt.wantErr != nil && (rec.Flushed || rec.Header().Get("Content-Type") != "") {
			t.Errorf("headers were sent although nothing reached the client")
		}
	})
}

func TestForward_StreamLimits(t *testing.T) {
	sleepThen := func(d time.Duration, s string) script {
		return func(_ context.Context, pw *io.PipeWriter) { time.Sleep(d); send(pw, s) }
	}
	tests := []limitCase{
		{name: "first byte timeout before headers is a 504", limits: StreamLimits{FirstByte: 5 * time.Second},
			headerDelay: time.Hour, body: stall, wantErr: ErrUpstreamTimeout, wantFinish: model.FinishTimeout, wantAfter: 5 * time.Second},
		{name: "first byte timeout after headers sends nothing", limits: StreamLimits{FirstByte: 5 * time.Second},
			headerDelay: time.Second, body: stall, wantErr: ErrUpstreamTimeout, wantFinish: model.FinishTimeout, wantAfter: 5 * time.Second},
		{name: "idle timeout mid-stream appends an openai error event", limits: StreamLimits{Idle: 3 * time.Second}, shape: normalise.ShapeOpenAI,
			body:       func(ctx context.Context, pw *io.PipeWriter) { send(pw, frameA); <-ctx.Done() },
			wantFinish: model.FinishTimeout, wantBody: frameA + "data: {", wantEvent: `"timeout_error"`, wantAfter: 3 * time.Second},
		{name: "idle timeout mid-stream appends an anthropic error event", limits: StreamLimits{Idle: 3 * time.Second}, shape: normalise.ShapeAnthropic,
			body:       func(ctx context.Context, pw *io.PipeWriter) { send(pw, frameA); <-ctx.Done() },
			wantFinish: model.FinishTimeout, wantBody: frameA + "event: error\n", wantEvent: `"timeout_error"`},
		{name: "max call duration cuts a stream that keeps talking", limits: StreamLimits{MaxCall: time.Second}, shape: normalise.ShapeOpenAI,
			body: func(ctx context.Context, pw *io.PipeWriter) {
				for ctx.Err() == nil {
					time.Sleep(400 * time.Millisecond)
					send(pw, frameA)
				}
			},
			wantFinish: model.FinishTimeout, wantBody: frameA + frameA, wantEvent: `"timeout_error"`, wantAfter: time.Second},
		{name: "steady chunks keep the idle and first byte timers quiet", limits: StreamLimits{FirstByte: 3 * time.Second, Idle: 3 * time.Second},
			body: func(_ context.Context, pw *io.PipeWriter) {
				for range 5 {
					time.Sleep(2 * time.Second)
					send(pw, frameA)
				}
			},
			wantFinish: model.FinishOK, wantBody: strings.Repeat(frameA, 5), wantAfter: 10 * time.Second},
		{name: "idle timeout on a non-SSE body cuts the connection", limits: StreamLimits{Idle: time.Second}, contentType: typeJSON,
			body:       func(ctx context.Context, pw *io.PipeWriter) { send(pw, `{"a":`); <-ctx.Done() },
			wantFinish: model.FinishTimeout, wantCut: true, wantBody: `{"a":`},
		{name: "upstream drop mid-stream appends an error event", shape: normalise.ShapeAnthropic,
			body:       func(_ context.Context, pw *io.PipeWriter) { send(pw, frameA); pw.CloseWithError(errors.New("reset")) },
			wantFinish: model.FinishUpstreamError, wantBody: frameA + "event: error\n", wantEvent: `"api_error"`},
		{name: "upstream drop before any byte is a 502", body: func(_ context.Context, pw *io.PipeWriter) { pw.CloseWithError(errors.New("reset")) },
			wantErr: ErrUpstreamUnreachable, wantFinish: model.FinishUpstreamError},
		{name: "upstream drop of a non-SSE body cuts the connection", contentType: typeJSON,
			body:       func(_ context.Context, pw *io.PipeWriter) { send(pw, "{"); pw.CloseWithError(errors.New("reset")) },
			wantFinish: model.FinishUpstreamError, wantCut: true, wantBody: "{"},
		{name: "first byte limit alone stops once bytes flow", limits: StreamLimits{FirstByte: 3 * time.Second},
			body: func(_ context.Context, pw *io.PipeWriter) {
				send(pw, frameA)
				time.Sleep(10 * time.Second)
				send(pw, frameA)
			},
			wantFinish: model.FinishOK, wantBody: frameA + frameA, wantAfter: 10 * time.Second},
		{name: "no limits means no timer", body: sleepThen(24*time.Hour, frameA), wantFinish: model.FinishOK, wantBody: frameA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runLimitCase(t, tt) })
	}
}

func TestForward_ClientCancelWithLimitsIsNotATimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		rt := rtFunc(func(r *http.Request) (*http.Response, error) {
			pr, pw := io.Pipe()
			go func() { send(pw, frameA); <-r.Context().Done(); pw.CloseWithError(r.Context().Err()) }()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: pr}, nil
		})
		go func() { time.Sleep(time.Second); cancel() }()
		target, _ := url.Parse("http://upstream/v1")
		rec := httptest.NewRecorder()
		res, err := Forward(ctx, rt, rec, Request{Method: "POST", Target: target, Limits: StreamLimits{Idle: time.Hour, MaxCall: time.Hour}})
		if err != nil || res.Finish != model.FinishClientCancel || res.Cut || rec.Body.String() != frameA {
			t.Fatalf("res=%+v err=%v body=%q", res, err, rec.Body.String())
		}
	})
}
