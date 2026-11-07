package normalise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ethosagent/tollgate/internal/model"
)

// ErrUnsupportedWire is returned by ForWire for a wire format that is only
// passed through (no normaliser).
var ErrUnsupportedWire = errors.New("normalise: no normaliser for wire format")

// EventMapper turns the SSE frames of one response into neutral Events. It is
// stateful (tool-call numbering, running usage), so use one per stream and
// never share it between goroutines.
type EventMapper interface {
	// Frame maps one SSE frame (event name, joined data lines). Unknown event
	// types yield no events and no error; malformed JSON yields an error.
	Frame(event string, data []byte) ([]model.Event, error)
	// Gap tells the mapper that n frames were dropped before the next one
	// (forward.FrameQueue reports it). The mapper keeps its state, but it can
	// no longer tell a truncated stream from a dropped finish frame, so
	// Finish then reports no truncation: the consumer takes the ending from
	// the queue's End.Finish instead.
	Gap(n int64)
	// Finish signals end of stream and returns any closing events: a single
	// EventError when frames were seen, no gap was reported, and the stream
	// ended before a finish reason (truncation). It is idempotent; later
	// calls return nil.
	Finish() []model.Event
}

// Wire is the normaliser for one provider wire format.
type Wire interface {
	// ParseRequest fills c.Model, c.Stream and c.Messages from a request body,
	// decoding the body exactly once. It leaves every other field untouched.
	ParseRequest(body []byte, c *model.Call) error
	// NewEventMapper returns a fresh mapper for one streamed response.
	NewEventMapper() EventMapper
	// MapBody maps a complete (stream:false) response body to the same events
	// a streamed equivalent would produce, so one consumer handles both.
	MapBody(body []byte) ([]model.Event, error)
}

// ForWire returns the normaliser for a wire format, or ErrUnsupportedWire.
func ForWire(f model.WireFormat) (Wire, error) {
	switch f {
	case model.WireOpenAIChat:
		return openAIWire{}, nil
	case model.WireAnthropicMessages:
		return anthropicWire{}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnsupportedWire, f)
}

// decodeObserver, when set by a test, runs on every strictDecode so tests can
// prove a body is decoded exactly once (HOT-4). Production leaves it nil.
var decodeObserver func()

// strictDecode is json.Unmarshal that also rejects an empty body. Every
// whole-body or whole-frame decode in this package goes through it.
func strictDecode(data []byte, v any) error {
	if decodeObserver != nil {
		decodeObserver()
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("normalise: empty JSON body")
	}
	return json.Unmarshal(data, v)
}

var (
	newline        = []byte("\n")
	carriageReturn = []byte("\r")
	space          = []byte(" ")
	eventPrefix    = []byte("event:")
	dataPrefix     = []byte("data:")
)

// SplitFrame extracts the event name and data from one SSE frame (the bytes
// between blank lines). Several data lines are joined with "\n"; comment
// lines are ignored.
func SplitFrame(frame []byte) (event string, data []byte) {
	first := true
	for len(frame) > 0 {
		var line []byte
		line, frame, _ = bytes.Cut(frame, newline)
		line = bytes.TrimSuffix(line, carriageReturn)
		if name, ok := bytes.CutPrefix(line, eventPrefix); ok {
			event = string(bytes.TrimPrefix(name, space))
		} else if v, ok := bytes.CutPrefix(line, dataPrefix); ok {
			v = bytes.TrimPrefix(v, space)
			if !first {
				data = append(data, '\n')
			}
			data = append(data, v...)
			first = false
		}
	}
	return event, data
}
