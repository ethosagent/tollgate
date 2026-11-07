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

type decodeFunc func(data []byte, v any) error

// ForWire returns the normaliser for a wire format, or ErrUnsupportedWire.
func ForWire(f model.WireFormat) (Wire, error) {
	switch f {
	case model.WireOpenAIChat:
		return openAIWire{decode: strictDecode}, nil
	case model.WireAnthropicMessages:
		return anthropicWire{decode: strictDecode}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnsupportedWire, f)
}

// strictDecode is json.Unmarshal that also rejects an empty body.
func strictDecode(data []byte, v any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("normalise: empty JSON body")
	}
	return json.Unmarshal(data, v)
}

// SplitFrame extracts the event name and data from one SSE frame (the bytes
// between blank lines). Several data lines are joined with "\n"; comment
// lines are ignored.
func SplitFrame(frame []byte) (event string, data []byte) {
	var first = true
	for len(frame) > 0 {
		var line []byte
		line, frame, _ = bytes.Cut(frame, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if name, ok := fieldValue(line, "event"); ok {
			event = string(name)
		} else if v, ok := fieldValue(line, "data"); ok {
			if !first {
				data = append(data, '\n')
			}
			data = append(data, v...)
			first = false
		}
	}
	return event, data
}

func fieldValue(line []byte, name string) ([]byte, bool) {
	rest, ok := bytes.CutPrefix(line, []byte(name+":"))
	if !ok {
		return nil, false
	}
	return bytes.TrimPrefix(rest, []byte(" ")), true
}

// usageEvent wraps a usage snapshot as an event.
func usageEvent(u model.Usage) model.Event {
	return model.Event{Kind: model.EventUsage, Usage: &u}
}

func errorEvent(msg string) model.Event {
	return model.Event{Kind: model.EventError, Err: msg}
}
