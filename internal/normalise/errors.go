package normalise

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ethosagent/tollgate/internal/model"
)

// ErrorShape is the JSON error body format a client library expects.
type ErrorShape int

// Error shapes.
const (
	ShapeGeneric ErrorShape = iota
	ShapeOpenAI
	ShapeAnthropic
	// ShapeOpenAIResponses is ShapeOpenAI for JSON bodies; its in-band stream
	// error is the typed "event: error" document the Responses API sends.
	ShapeOpenAIResponses
)

// ShapeForWire picks the error shape for a wire format: OpenAI chat gets
// the OpenAI shape, Responses its own variant, Anthropic messages the Anthropic shape, anything else the
// generic one.
func ShapeForWire(w model.WireFormat) ErrorShape {
	switch w {
	case model.WireOpenAIChat:
		return ShapeOpenAI
	case model.WireOpenAIResponses:
		return ShapeOpenAIResponses
	case model.WireAnthropicMessages:
		return ShapeAnthropic
	}
	return ShapeGeneric
}

// openAITypes maps a status to the OpenAI error type; the generic shape uses
// the same names. anthropicOverrides lists only where Anthropic differs. A
// status not listed gets the shape's fallback type.
var (
	openAITypes = map[int]string{
		http.StatusBadRequest:            "invalid_request_error",
		http.StatusNotFound:              "invalid_request_error",
		http.StatusRequestEntityTooLarge: "invalid_request_error",
		http.StatusUnauthorized:          "authentication_error",
		http.StatusForbidden:             "permission_error",
		http.StatusTooManyRequests:       "rate_limit_error",
		http.StatusGatewayTimeout:        "timeout_error",
	}
	anthropicOverrides = map[int]string{
		http.StatusNotFound:              "not_found_error",
		http.StatusRequestEntityTooLarge: "request_too_large",
	}
)

func errorType(shape ErrorShape, status int) string {
	if shape == ShapeAnthropic {
		if t, ok := anthropicOverrides[status]; ok {
			return t
		}
	}
	if t, ok := openAITypes[status]; ok {
		return t
	}
	if shape == ShapeAnthropic {
		return "api_error"
	}
	return "server_error"
}

// statusMessages is the fixed client-facing message per status (ADR 0008):
// a rejection never carries an extension's or the transport's error text.
var statusMessages = map[int]string{
	http.StatusBadRequest:            "bad request",
	http.StatusUnauthorized:          "authentication failed",
	http.StatusForbidden:             "permission denied",
	http.StatusNotFound:              "unknown provider or upstream",
	http.StatusRequestEntityTooLarge: "request body too large",
	http.StatusTooManyRequests:       "too many requests",
	http.StatusInternalServerError:   "internal error",
	http.StatusBadGateway:            "upstream error",
	http.StatusServiceUnavailable:    "service unavailable",
	http.StatusGatewayTimeout:        "upstream timed out",
}

// StatusMessage returns the fixed message for status: the table above, else
// the lower-cased standard status text. It never depends on an error value.
func StatusMessage(status int) string {
	if m, ok := statusMessages[status]; ok {
		return m
	}
	if t := http.StatusText(status); t != "" {
		return strings.ToLower(t)
	}
	return "error"
}

// FinishStatus is the status that describes a stream that ended with f after
// it started: 504 for a timeout, else 502. The SSE error event uses it.
func FinishStatus(f model.Finish) int {
	if f == model.FinishTimeout {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// errorBody is the JSON error document for a shape.
func errorBody(shape ErrorShape, status int, msg string) any {
	switch shape {
	case ShapeAnthropic:
		return map[string]any{
			"type":  "error",
			"error": map[string]string{"type": errorType(shape, status), "message": msg},
		}
	case ShapeOpenAI, ShapeOpenAIResponses:
		return map[string]any{
			"error": map[string]any{"message": msg, "type": errorType(shape, status), "param": nil, "code": nil},
		}
	}
	return map[string]any{
		"error": map[string]string{"type": errorType(shape, status), "message": msg},
	}
}

// WriteError writes a JSON error body in the given shape. The error type is
// derived from status so callers cannot pick one the shape does not know.
// Set any extra headers (Retry-After) before calling it.
func WriteError(w http.ResponseWriter, shape ErrorShape, status int, msg string) {
	body := errorBody(shape, status, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // the status line is already sent; nothing to recover
}

// AppendErrorEvent appends one SSE frame carrying an error to dst, for a
// stream that has already started and so cannot change its status. Anthropic
// and Responses streams get an "event: error" frame; the others a bare data frame holding
// the same JSON document WriteError would send. status picks the error type
// exactly as in WriteError.
func AppendErrorEvent(dst []byte, shape ErrorShape, status int, msg string) []byte {
	if shape == ShapeOpenAIResponses {
		doc, _ := json.Marshal(map[string]any{"type": "error", "code": errorType(shape, status), "message": msg, "param": nil})
		dst = append(dst, "event: error\ndata: "...)
		dst = append(dst, doc...)
		return append(dst, "\n\n"...)
	}
	doc, _ := json.Marshal(errorBody(shape, status, msg)) // maps of strings and nil cannot fail
	if shape == ShapeAnthropic {
		dst = append(dst, "event: error\n"...)
	}
	dst = append(dst, "data: "...)
	dst = append(dst, doc...)
	return append(dst, "\n\n"...)
}
