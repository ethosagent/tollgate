package normalise

import (
	"bytes"
	"strconv"
)

// shiftTopInt returns raw (one SSE frame) with the digits of data's top-level
// integer member key replaced by their value plus by, and the new value. data
// is the frame's data payload, found verbatim inside raw. Every other byte of
// raw is untouched (key order, spacing, escapes, "<>&", the event line and the
// terminator), because a recovered frame reaches the client as written. ok is
// false when raw does not hold data verbatim (multi-line data), the key is
// absent at the top level, or its value is not a plain integer.
func shiftTopInt(raw, data []byte, key string, by int) (out []byte, val int, ok bool) {
	base := bytes.Index(raw, data)
	if base < 0 {
		return nil, 0, false
	}
	start, end, old, found := topIntSpan(data, key)
	if !found {
		return nil, 0, false
	}
	val = old + by
	out = make([]byte, 0, len(raw)+4)
	out = append(out, raw[:base+start]...)
	out = strconv.AppendInt(out, int64(val), 10)
	out = append(out, raw[base+end:]...)
	return out, val, true
}

// topIntSpan scans the JSON object d once, tracking nesting and strings, and
// returns the byte span and value of the integer that is the value of the
// top-level member key. A key name inside a string value or a nested object
// is not a match.
func topIntSpan(d []byte, key string) (start, end, val int, ok bool) {
	depth := 0
	for i := 0; i < len(d); i++ {
		switch d[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '"':
			j := i + 1
			for j < len(d) && d[j] != '"' {
				if d[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(d) {
				return 0, 0, 0, false
			}
			if depth == 1 && string(d[i+1:j]) == key {
				if s, e, v, found := intAfterColon(d, j+1); found {
					return s, e, v, true
				}
			}
			i = j
		}
	}
	return 0, 0, 0, false
}

// intAfterColon reads ` : 123` starting at i: the span and value of the
// non-negative integer, found only when a colon follows (so a string that
// merely equals the key, as a value, is not taken) and the number has no
// fraction or exponent.
func intAfterColon(d []byte, i int) (start, end, val int, ok bool) {
	i = skipSpace(d, i)
	if i >= len(d) || d[i] != ':' {
		return 0, 0, 0, false
	}
	start = skipSpace(d, i+1)
	end = start
	for end < len(d) && d[end] >= '0' && d[end] <= '9' {
		val = val*10 + int(d[end]-'0')
		end++
	}
	if end == start || end-start > 9 || (end < len(d) && (d[end] == '.' || d[end] == 'e' || d[end] == 'E')) {
		return 0, 0, 0, false
	}
	return start, end, val, true
}

func skipSpace(d []byte, i int) int {
	for i < len(d) && (d[i] == ' ' || d[i] == '\t' || d[i] == '\n' || d[i] == '\r') {
		i++
	}
	return i
}
