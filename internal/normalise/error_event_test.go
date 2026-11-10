package normalise

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func TestAppendErrorEvent_Shapes(t *testing.T) {
	tests := []struct {
		name      string
		shape     ErrorShape
		status    int
		wantEvent string // "" means no event: line
		wantType  string
	}{
		{"anthropic timeout", ShapeAnthropic, 504, "error", "timeout_error"},
		{"anthropic upstream drop", ShapeAnthropic, 502, "error", "api_error"},
		{"openai timeout", ShapeOpenAI, 504, "", "timeout_error"},
		{"openai upstream drop", ShapeOpenAI, 502, "", "server_error"},
		{"generic timeout", ShapeGeneric, 504, "", "timeout_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AppendErrorEvent([]byte("pre"), tt.shape, tt.status, "boom")
			frame, ok := strings.CutPrefix(string(got), "pre")
			if !ok || !strings.HasSuffix(frame, "\n\n") {
				t.Fatalf("frame = %q", frame)
			}
			event, data := SplitFrame([]byte(strings.TrimSuffix(frame, "\n\n")))
			if event != tt.wantEvent {
				t.Fatalf("event = %q, want %q", event, tt.wantEvent)
			}
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatalf("data not JSON: %v (%s)", err, data)
			}
			e, _ := body["error"].(map[string]any)
			if e["type"] != tt.wantType || e["message"] != "boom" {
				t.Fatalf("body = %v", body)
			}
		})
	}
}

// TestAppendErrorEvent_ResponsesShape: a Responses stream gets the error
// event the API itself sends: "event: error" and a typed document with the
// error type as its code. Its JSON error body (pre-stream) stays OpenAI-shaped.
func TestAppendErrorEvent_ResponsesShape(t *testing.T) {
	if got := ShapeForWire(model.WireOpenAIResponses); got != ShapeOpenAIResponses {
		t.Fatalf("ShapeForWire(responses) = %v, want ShapeOpenAIResponses", got)
	}
	frame := string(AppendErrorEvent(nil, ShapeOpenAIResponses, 502, "upstream error"))
	name, data := SplitFrame([]byte(strings.TrimSuffix(frame, "\n\n")))
	if name != "error" || !strings.HasSuffix(frame, "\n\n") {
		t.Fatalf("frame = %q, want an event: error frame", frame)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["type"] != "error" || doc["code"] != "server_error" || doc["message"] != "upstream error" {
		t.Fatalf("doc = %v", doc)
	}
	if _, ok := doc["param"]; !ok {
		t.Fatalf("doc = %v, want param (null) like the API's event", doc)
	}
	// The same JSON error body as chat before the stream starts.
	rec := httptest.NewRecorder()
	WriteError(rec, ShapeOpenAIResponses, 502, "upstream error")
	var body struct {
		Error struct{ Type, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Type != "server_error" {
		t.Fatalf("body = %s err %v", rec.Body.Bytes(), err)
	}
}
