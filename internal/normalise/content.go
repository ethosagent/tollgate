package normalise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// decodeContent decodes message content that is a string, an array of typed
// parts, or null. A string becomes one part built by fromText. It is shared by
// both request wires, so a fix lands once.
func decodeContent[T any](b []byte, fromText func(string) T) ([]T, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, errContent
	}
	switch b[0] {
	case 'n':
		if string(b) == "null" {
			return nil, nil
		}
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		return []T{fromText(s)}, nil
	case '[':
		var parts []T
		if err := json.Unmarshal(b, &parts); err != nil {
			return nil, err
		}
		return parts, nil
	}
	return nil, errContent
}

var errContent = errors.New("content must be a string, array or null")

// wrapRequestErr prefixes a request decode error with its wire.
func wrapRequestErr(what string, err error) error { return fmt.Errorf("%s: %w", what, err) }
