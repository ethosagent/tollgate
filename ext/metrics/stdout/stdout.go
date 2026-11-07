package stdout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ethosagent/tollgate"
)

var _ tollgate.MetricSink = (*Sink)(nil)

// Sink writes metrics as JSON Lines to a writer.
type Sink struct {
	mu sync.Mutex // one batch at a time keeps lines whole
	w  io.Writer
}

// New returns a Sink that writes to w.
func New(w io.Writer) *Sink { return &Sink{w: w} }

// line is the JSON shape of one Metric.
type line struct {
	CallID       string `json:"call_id"`
	Tenant       string `json:"tenant"`
	Session      string `json:"session"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Stream       bool   `json:"stream"`
	Status       int    `json:"status"`
	Finish       string `json:"finish"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CachedTokens int64  `json:"cached_input_tokens"`
	Reasoning    int64  `json:"reasoning_tokens"`
	Start        string `json:"start"`
	TTFBMillis   int64  `json:"ttfb_ms"`
	DurMillis    int64  `json:"duration_ms"`
}

func lineOf(m tollgate.Metric) line {
	return line{CallID: m.CallID, Tenant: m.TenantID, Session: m.SessionID, Provider: m.Provider, Model: m.Model,
		Stream: m.Stream, Status: m.Status, Finish: string(m.Finish),
		InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens,
		CachedTokens: m.Usage.CachedInputTokens, Reasoning: m.Usage.ReasoningTokens,
		Start: m.Start.UTC().Format(time.RFC3339Nano), TTFBMillis: m.TTFB.Milliseconds(), DurMillis: m.Duration.Milliseconds()}
}

// Write implements tollgate.MetricSink.
func (s *Sink) Write(ctx context.Context, batch []tollgate.Metric) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(batch) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // Encode ends each value with a newline
	for _, m := range batch {
		if err := enc.Encode(lineOf(m)); err != nil {
			return fmt.Errorf("stdout sink: encode: %w", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("stdout sink: write: %w", err)
	}
	return nil
}
