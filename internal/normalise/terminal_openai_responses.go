package normalise

import (
	"bytes"
	"encoding/json"
)

// responsesTerminal: the region opens at response.completed,
// response.incomplete or response.failed, the one frame that carries the
// status and usage. A bare "error" event does not open it, as for Anthropic:
// the stream then ends without a finish and the barrier reports that.
type responsesTerminal struct{}

var (
	responsesTypePrefix = []byte(`{"type":"`)
	typeCompleted       = []byte("response.completed")
	typeIncomplete      = []byte("response.incomplete")
	typeFailed          = []byte("response.failed")
)

// Opens uses the SSE event name when there is one. Without it the JSON "type"
// must be the first key, as OpenAI writes it; a frame that hides it later
// reads as not terminal (the stream's end still closes the region), and
// message text cannot fake it because only the leading bytes are read.
func (responsesTerminal) Opens(event string, data []byte) bool {
	if event != "" {
		return isTerminalName(event)
	}
	rest, ok := bytes.CutPrefix(data, responsesTypePrefix)
	if !ok {
		return false
	}
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return false
	}
	name := rest[:end]
	return bytes.Equal(name, typeCompleted) || bytes.Equal(name, typeIncomplete) || bytes.Equal(name, typeFailed)
}

// isTerminalName reports whether t ends a response.
func isTerminalName(t string) bool {
	switch t {
	case "response.completed", "response.incomplete", "response.failed":
		return true
	}
	return false
}

func (responsesTerminal) NewSplicer() Splicer { return &responsesSplicer{} }

// responsesSplicer makes one response from several streams: a recovery's
// response.created and response.in_progress are dropped (the client has a
// response) and its output_index values continue after the items the client
// already has. sequence_number is not rewritten: clients order by arrival.
type responsesSplicer struct {
	next   int // output_index the next new item must have
	offset int // added to the current recovery's indexes
}

var outputItemAdded = []byte("output_item.added")

func (s *responsesSplicer) Begin() { s.offset = s.next }

func (s *responsesSplicer) Sent(event string, data []byte) {
	// Cheap filter first (Sent sees every frame the client gets), then the
	// decoded type, so spacing or key order added by a gateway does not matter.
	if event != "response.output_item.added" && (event != "" || !bytes.Contains(data, outputItemAdded) || responsesType("", data) != "response.output_item.added") {
		return
	}
	var e struct {
		OutputIndex int `json:"output_index"`
	}
	if json.Unmarshal(data, &e) == nil {
		s.next = max(s.next, e.OutputIndex+1)
	}
}

// Recovery shifts the output_index of a recovery frame by the offset and
// changes nothing else in it (see shiftIndex); a frame without one passes on
// unchanged.
func (s *responsesSplicer) Recovery(raw []byte, event string, data []byte) ([]byte, bool) {
	switch responsesType(event, data) {
	case "response.created", "response.in_progress":
		return nil, false
	}
	out, idx, ok := shiftIndex(raw, event, data, "output_index", s.offset)
	if !ok {
		return raw, true
	}
	if responsesType(event, data) == "response.output_item.added" {
		s.next = max(s.next, idx+1)
	}
	return out, true
}

// responsesType is the SSE event name, or the JSON "type" when there is none.
func responsesType(event string, data []byte) string {
	if event != "" {
		return event
	}
	var e struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &e)
	return e.Type
}
