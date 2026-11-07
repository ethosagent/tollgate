package normalise

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestWriteError_StatusMatrix pins both provider shapes for every status the
// proxy emits: 401/403/404/413/429/500/504.
func TestWriteError_StatusMatrix(t *testing.T) {
	statuses := []int{401, 403, 404, 413, 429, 500, 504}
	for _, shape := range []ErrorShape{ShapeOpenAI, ShapeAnthropic} {
		for _, status := range statuses {
			rec := httptest.NewRecorder()
			WriteError(rec, shape, status, "msg")
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("shape %d status %d: invalid JSON: %v", shape, status, err)
			}
			if rec.Code != status {
				t.Fatalf("shape %d: status %d, want %d", shape, rec.Code, status)
			}
			inner, ok := body["error"].(map[string]any)
			if !ok || inner["message"] != "msg" || inner["type"] == "" || inner["type"] == nil {
				t.Fatalf("shape %d status %d: bad body %s", shape, status, rec.Body.String())
			}
			if shape == ShapeAnthropic && body["type"] != "error" {
				t.Fatalf("anthropic status %d: top-level type = %v", status, body["type"])
			}
			if shape == ShapeOpenAI {
				if _, has := inner["code"]; !has {
					t.Fatalf("openai status %d: missing code key in %s", status, rec.Body.String())
				}
			}
		}
	}
}
