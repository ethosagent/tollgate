package normalise

import (
	"bytes"
	"encoding/json"
)

// Lenient field types of a Responses request item. Each decodes straight to
// what the builder needs, in the one pass that reads the item, and never fails
// the parse for a shape it does not model (see orItem).

// orText is a content or output field read straight to its text: a string, an
// array of typed parts (joined) or null. Any other shape sets bad instead of
// failing the decode: only the item types that read the field reject it (a
// message with object content is an error; an unmodelled item is skipped).
type orText struct {
	s   string
	bad bool
}

func (t *orText) UnmarshalJSON(b []byte) error {
	if s, ok := plainString(b); ok {
		t.s = s
		return nil
	}
	var c oaContent
	if err := c.UnmarshalJSON(b); err != nil {
		t.bad = true
		return nil
	}
	t.s = c.text()
	return nil
}

// read returns the text, or the content error for a field of the wrong shape.
func (t orText) read() (string, error) {
	if t.bad {
		return "", errContent
	}
	return t.s, nil
}

// plainString returns the text of a JSON string without escapes, which is the
// usual case and needs no second decode. b is already validated JSON.
func plainString(b []byte) (string, bool) {
	if n := len(b); n >= 2 && b[0] == '"' && bytes.IndexByte(b, '\\') < 0 {
		return string(b[1 : n-1]), true
	}
	return "", false
}

// orParts is a reasoning summary: typed text parts. A wrong shape sets bad;
// only a reasoning item rejects it.
type orParts struct {
	parts []oaPart
	bad   bool
}

func (p *orParts) UnmarshalJSON(b []byte) error {
	if json.Unmarshal(b, &p.parts) != nil {
		p.parts, p.bad = nil, true
	}
	return nil
}

// orArgs is function_call arguments: the JSON text in a string. A value of
// another type (tool_search_call carries an object) is ignored, never an error.
type orArgs json.RawMessage

func (a *orArgs) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || b[0] != '"' {
		return nil
	}
	s, ok := plainString(b)
	if !ok && json.Unmarshal(b, &s) != nil {
		return nil
	}
	*a = orArgs(rawArguments(s))
	return nil
}
