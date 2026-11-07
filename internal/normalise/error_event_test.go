package normalise

import (
	"encoding/json"
	"strings"
	"testing"
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
