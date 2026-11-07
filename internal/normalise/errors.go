package normalise

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ErrorShape is the JSON error body format a client library expects.
type ErrorShape int

// Error shapes.
const (
	ShapeGeneric ErrorShape = iota
	ShapeOpenAI
	ShapeAnthropic
)

// ShapeForPath picks the error shape from the first path segment: /openai
// gets the OpenAI shape, /anthropic the Anthropic shape, anything else the
// generic one.
func ShapeForPath(path string) ErrorShape {
	seg, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	switch seg {
	case "openai":
		return ShapeOpenAI
	case "anthropic":
		return ShapeAnthropic
	}
	return ShapeGeneric
}

// WriteError writes a JSON error body in the given shape. The error type is
// derived from status so callers cannot pick one the shape does not know.
// Set any extra headers (Retry-After) before calling it.
func WriteError(w http.ResponseWriter, shape ErrorShape, status int, msg string) {
	var body any
	switch shape {
	case ShapeAnthropic:
		body = map[string]any{
			"type":  "error",
			"error": map[string]string{"type": anthropicType(status), "message": msg},
		}
	case ShapeOpenAI:
		body = map[string]any{
			"error": map[string]any{"message": msg, "type": openAIType(status), "param": nil, "code": nil},
		}
	default:
		body = map[string]any{
			"error": map[string]string{"type": openAIType(status), "message": msg},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // the status line is already sent; nothing to recover
}

func anthropicType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		return "timeout_error"
	}
	return "api_error"
}

func openAIType(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		return "timeout_error"
	}
	return "server_error"
}
