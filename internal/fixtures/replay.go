package fixtures

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Default picks the fixture a fake upstream replays for a request that names
// none: the wire comes from the path suffix and the variant from the body's
// stream flag. A body that is not JSON counts as stream:false. It returns ""
// for a path it does not know. testkit and the core's tests share it, so both
// fakes replay the same thing.
func Default(path string, body []byte) string {
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	switch {
	case strings.HasSuffix(path, OpenAIChatPath) && req.Stream:
		return "openai/chat-stream-text"
	case strings.HasSuffix(path, OpenAIChatPath):
		return "openai/chat"
	case strings.HasSuffix(path, AnthropicMessagesPath) && req.Stream:
		return "anthropic/messages-stream-text"
	case strings.HasSuffix(path, AnthropicMessagesPath):
		return "anthropic/messages"
	}
	return ""
}

// Wait sleeps for d and reports whether it did: false if ctx ended first.
// d <= 0 returns at once (true unless ctx is already done). Fake upstreams use
// it for first-byte and per-chunk delays that a cancelled client cuts short.
func Wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
