package normalise

import (
	"bytes"
	"fmt"

	"github.com/ethosagent/tollgate/internal/model"
)

// Terminal knows, for one wire format, where the end of a streamed response
// starts and how a recovery stream is spliced onto the response the client
// already received (ARCHITECTURE.md section 5, seam 8; ADR 0004). Its
// methods are cheap byte checks: they run on the client's byte path.
type Terminal interface {
	// Opens reports whether the frame (event name, joined data lines) opens
	// the terminal region: the first frame that carries the finish reason,
	// or the closing frame when none did. The region is that frame and every
	// frame after it. A false answer never needs a full parse.
	Opens(event string, data []byte) bool
	// NewSplicer returns a fresh Splicer for one client response.
	NewSplicer() Splicer
}

// Splicer makes the frames of recovery streams read as one response. One
// Splicer serves a whole client response, original stream first.
type Splicer interface {
	// Sent records a frame of the original stream that went to the client.
	Sent(event string, data []byte)
	// Begin starts a recovery stream.
	Begin()
	// Recovery maps one frame of the current recovery stream, from before
	// its terminal region. raw is the frame as received, with its closing
	// blank line. It returns the bytes to send, raw itself when unchanged,
	// and false to drop the frame.
	Recovery(raw []byte, event string, data []byte) (out []byte, keep bool)
}

// TerminalFor returns the Terminal of a wire format, or ErrUnsupportedWire.
func TerminalFor(f model.WireFormat) (Terminal, error) {
	switch f {
	case model.WireOpenAIChat:
		return openAITerminal{}, nil
	case model.WireOpenAIResponses:
		return responsesTerminal{}, nil
	case model.WireAnthropicMessages:
		return anthropicTerminal{}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnsupportedWire, f)
}

var finishReasonKey = []byte(`"finish_reason"`)

// hasFinishReason reports whether a chunk sets a non-null finish_reason. An
// unescaped quote cannot occur inside a JSON string, so the key bytes cannot
// come from message text; only the first occurrence is looked at, and only
// whitespace and the colon are skipped to the value.
func hasFinishReason(data []byte) bool {
	for {
		i := bytes.Index(data, finishReasonKey)
		if i < 0 {
			return false
		}
		data = data[i+len(finishReasonKey):]
		rest := bytes.TrimLeft(data, " \t\r\n")
		if len(rest) == 0 || rest[0] != ':' {
			continue // the key bytes were a value, not a key
		}
		rest = bytes.TrimLeft(rest[1:], " \t\r\n")
		if len(rest) > 0 && rest[0] == '"' {
			return true
		}
	}
}
