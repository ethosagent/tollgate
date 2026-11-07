package normalise

import (
	"bytes"
	"encoding/json"
)

// anthropicTerminal: the region opens at message_delta (stop reason and
// usage), so message_stop is held with it.
type anthropicTerminal struct{}

func (anthropicTerminal) Opens(event string, data []byte) bool {
	switch eventType(event, data) {
	case "message_delta", "message_stop":
		return true
	}
	return false
}

func (anthropicTerminal) NewSplicer() Splicer { return &anthropicSplicer{} }

// eventType is the SSE event name, or the JSON "type" when the stream sends
// no event lines.
func eventType(event string, data []byte) string {
	if event != "" {
		return event
	}
	for _, t := range []string{"message_start", "message_delta", "message_stop", "ping", "content_block_start", "content_block_delta", "content_block_stop"} {
		if bytes.Contains(data, []byte(`"type":"`+t+`"`)) {
			return t
		}
	}
	return ""
}

// anthropicSplicer makes one message from several streams: a recovery's
// message_start and ping are dropped (the client has a message) and its
// content block indexes continue after the blocks the client already has.
type anthropicSplicer struct {
	next   int // index the next new content block must have
	offset int // added to the current recovery's indexes
}

func (s *anthropicSplicer) Begin() { s.offset = s.next }

func (s *anthropicSplicer) Sent(event string, data []byte) {
	if eventType(event, data) != "content_block_start" {
		return
	}
	var e struct {
		Index int `json:"index"`
	}
	if json.Unmarshal(data, &e) == nil {
		s.next = max(s.next, e.Index+1)
	}
}

func (s *anthropicSplicer) Recovery(raw []byte, event string, data []byte) ([]byte, bool) {
	switch t := eventType(event, data); t {
	case "message_start", "ping":
		return nil, false
	case "content_block_start", "content_block_delta", "content_block_stop":
		return s.shift(raw, t, data), true
	}
	return raw, true
}

// shift rewrites the index of a content block frame by the recovery offset.
// A frame it cannot decode is passed on unchanged.
func (s *anthropicSplicer) shift(raw []byte, typ string, data []byte) []byte {
	var m map[string]json.RawMessage
	var idx int
	if json.Unmarshal(data, &m) != nil || json.Unmarshal(m["index"], &idx) != nil {
		return raw
	}
	idx += s.offset
	m["index"] = json.RawMessage(itoa(idx))
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	if typ == "content_block_start" {
		s.next = max(s.next, idx+1)
	}
	return append(append([]byte("event: "+typ+"\ndata: "), out...), '\n', '\n')
}
