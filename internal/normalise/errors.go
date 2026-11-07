package normalise

import (
	"encoding/json"
	"net/http"

	"github.com/ethosagent/tollgate/internal/model"
)

// ErrorShape is the JSON error body format a client library expects.
type ErrorShape int

// Error shapes.
const (
	ShapeGeneric ErrorShape = iota
	ShapeOpenAI
	ShapeAnthropic
)

// ShapeForWire picks the error shape for a wire format: OpenAI chat gets the
// OpenAI shape, Anthropic messages the Anthropic shape, anything else the
// generic one.
func ShapeForWire(w model.WireFormat) ErrorShape {
	switch w {
	case model.WireOpenAIChat:
		return ShapeOpenAI
	case model.WireAnthropicMessages:
		return ShapeAnthropic
	}
	return ShapeGeneric
}

// errorTypes maps a status to the error type each wire uses; the generic
// shape uses the OpenAI names. A status not listed gets the fallback.
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
	anthropicTypes = map[int]string{
		http.StatusBadRequest:            "invalid_request_error",
		http.StatusNotFound:              "not_found_error",
		http.StatusRequestEntityTooLarge: "request_too_large",
		http.StatusUnauthorized:          "authentication_error",
		http.StatusForbidden:             "permission_error",
		http.StatusTooManyRequests:       "rate_limit_error",
		http.StatusGatewayTimeout:        "timeout_error",
	}
)

func errorType(types map[int]string, status int, fallback string) string {
	if t, ok := types[status]; ok {
		return t
	}
	return fallback
}

// errorBody is the JSON error document for a shape.
func errorBody(shape ErrorShape, status int, msg string) any {
	switch shape {
	case ShapeAnthropic:
		return map[string]any{
			"type":  "error",
			"error": map[string]string{"type": errorType(anthropicTypes, status, "api_error"), "message": msg},
		}
	case ShapeOpenAI:
		return map[string]any{
			"error": map[string]any{"message": msg, "type": errorType(openAITypes, status, "server_error"), "param": nil, "code": nil},
		}
	}
	return map[string]any{
		"error": map[string]string{"type": errorType(openAITypes, status, "server_error"), "message": msg},
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
// streams get an "event: error" frame; the others a bare data frame holding
// the same JSON document WriteError would send. status picks the error type
// exactly as in WriteError.
func AppendErrorEvent(dst []byte, shape ErrorShape, status int, msg string) []byte {
	doc, _ := json.Marshal(errorBody(shape, status, msg)) // maps of strings and nil cannot fail
	if shape == ShapeAnthropic {
		dst = append(dst, "event: error\n"...)
	}
	dst = append(dst, "data: "...)
	dst = append(dst, doc...)
	return append(dst, "\n\n"...)
}
