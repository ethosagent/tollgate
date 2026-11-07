package testkit_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// Config.Upstream faults reach the plugin through the real proxy: each case
// checks what the client got and how After saw the call end.
func TestRunWith_UpstreamFaults(t *testing.T) {
	const fx = "openai/chat-stream-text"
	chunks := testkit.Chunks(fx)
	tests := []struct {
		name       string
		upstream   testkit.Options
		limits     tollgate.Limits
		wantStatus int
		wantFinish tollgate.Finish
		check      func(t *testing.T, body []byte)
	}{
		{
			name:     "a cut stream keeps the sent chunks and ends in an error event",
			upstream: testkit.Options{FailAfterChunks: 2}, wantStatus: 200, wantFinish: tollgate.FinishUpstreamError,
			check: func(t *testing.T, body []byte) {
				sent := bytes.Join(chunks[:2], nil)
				if !bytes.HasPrefix(body, sent) || !bytes.Contains(body[len(sent):], []byte(`"error"`)) {
					t.Fatalf("body = %q, want the first two chunks then a provider error event", body)
				}
			},
		},
		{
			name: "a status override is relayed as is", upstream: testkit.Options{Status: 503},
			wantStatus: 503, wantFinish: tollgate.FinishUpstreamError,
		},
		{
			name:       "latency within the limits changes nothing",
			upstream:   testkit.Options{FirstByteDelay: 5 * time.Millisecond, ChunkDelay: time.Millisecond},
			wantStatus: 200, wantFinish: tollgate.FinishOK,
			check: func(t *testing.T, body []byte) {
				if want := bytes.Join(chunks, nil); !bytes.Equal(body, want) {
					t.Fatalf("body = %q, want the fixture byte for byte", body)
				}
			},
		},
		{
			name:     "a first byte later than FirstByteTimeout times out",
			upstream: testkit.Options{FirstByteDelay: time.Second}, limits: tollgate.Limits{FirstByteTimeout: 20 * time.Millisecond},
			wantStatus: 504, wantFinish: tollgate.FinishTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := testkit.RunWith(t, testkit.Config{Plugins: []any{probeHook{}}, Fixtures: []string{fx},
				Upstream: tt.upstream, Limits: tt.limits})
			if got := rec.Replies[0].Status; got != tt.wantStatus {
				t.Fatalf("status = %d, want %d", got, tt.wantStatus)
			}
			after := rec.Of(testkit.StageAfter)
			if len(after) != 1 || after[0].Metric.Finish != tt.wantFinish {
				t.Fatalf("After = %+v, want one call ending %q", after, tt.wantFinish)
			}
			if tt.check != nil {
				tt.check(t, rec.Replies[0].Body)
			}
		})
	}
}
