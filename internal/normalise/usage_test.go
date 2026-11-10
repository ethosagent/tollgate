package normalise

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func TestTrackUsage_KeepsTheLastRunningTotal(t *testing.T) {
	u := model.Usage{InputTokens: 1}
	TrackUsage(&u, []model.Event{{Kind: model.EventTextDelta}, {Kind: model.EventUsage}}) // nil Usage ignored
	if u.InputTokens != 1 {
		t.Fatalf("u = %+v, want unchanged", u)
	}
	TrackUsage(&u, []model.Event{
		{Kind: model.EventUsage, Usage: &model.Usage{InputTokens: 5}},
		{Kind: model.EventUsage, Usage: &model.Usage{InputTokens: 5, OutputTokens: 9}},
	})
	if u != (model.Usage{InputTokens: 5, OutputTokens: 9}) {
		t.Fatalf("u = %+v, want the last total", u)
	}
}

// TestAnthropicUsage_CacheWriteTokens: cache_creation_input_tokens lands in
// Usage.CacheWriteInputTokens, streamed (message_start survives the
// message_delta overlay) and buffered. The OpenAI wires never set it.
func TestAnthropicUsage_CacheWriteTokens(t *testing.T) {
	want := model.Usage{InputTokens: 12, OutputTokens: 2, CachedInputTokens: 300, CacheWriteInputTokens: 2048}
	for _, name := range []string{"anthropic/messages-stream-cache", "anthropic/messages-cache"} {
		t.Run(name, func(t *testing.T) {
			var got model.Usage
			TrackUsage(&got, eventsOf(t, name))
			if got != want {
				t.Fatalf("usage = %+v, want %+v", got, want)
			}
		})
	}
	for _, name := range []string{"openai/chat-stream-text", "openai/responses-stream-text"} {
		var got model.Usage
		TrackUsage(&got, eventsOf(t, name))
		if got.CacheWriteInputTokens != 0 {
			t.Errorf("%s: CacheWriteInputTokens = %d, want 0", name, got.CacheWriteInputTokens)
		}
	}
}
