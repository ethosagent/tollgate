package normalise

import (
	"encoding/json"
	"strconv"

	"github.com/ethosagent/tollgate/internal/model"
)

func itoa(i int) []byte { return strconv.AppendInt(nil, int64(i), 10) }

// Summarizer builds the model.StreamSummary a Finalizer decides on from the
// frames of one upstream response, through the wire's EventMapper. Like the
// mapper it is single-goroutine and single-stream.
type Summarizer struct {
	m     EventMapper
	sum   model.StreamSummary
	tools []model.ToolCall
	args  [][]byte
}

// NewSummarizer returns a Summarizer for one streamed response of wire w.
func NewSummarizer(w Wire) *Summarizer { return &Summarizer{m: w.NewEventMapper()} }

// Frame folds one SSE frame into the summary. A frame that does not parse is
// skipped, as the Tap does.
func (z *Summarizer) Frame(event string, data []byte) {
	evs, err := z.m.Frame(event, data)
	if err != nil {
		return
	}
	z.sum.Events += len(evs)
	TrackUsage(&z.sum.Usage, evs)
	for i := range evs {
		switch e := &evs[i]; e.Kind {
		case model.EventFinish:
			z.sum.FinishReason = e.FinishReason
		case model.EventToolCallDelta:
			z.tool(e.ToolCall)
		}
	}
}

// tool merges a tool-call delta into its call; ordinals are dense from 0.
func (z *Summarizer) tool(d *model.ToolCallDelta) {
	for len(z.tools) <= d.Index {
		z.tools = append(z.tools, model.ToolCall{})
		z.args = append(z.args, nil)
	}
	t := &z.tools[d.Index]
	if d.ID != "" {
		t.ID = d.ID
	}
	if d.Name != "" {
		t.Name = d.Name
	}
	z.args[d.Index] = append(z.args[d.Index], d.Arguments...)
}

// Summary returns the summary so far; its slices are the caller's.
func (z *Summarizer) Summary() model.StreamSummary {
	s := z.sum
	for i, t := range z.tools {
		if len(z.args[i]) > 0 {
			t.Arguments = json.RawMessage(append([]byte(nil), z.args[i]...))
		}
		s.ToolCalls = append(s.ToolCalls, t)
	}
	return s
}
