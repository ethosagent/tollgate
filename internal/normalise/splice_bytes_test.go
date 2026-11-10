package normalise

import (
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// TestSplicer_RewritesOnlyTheIndexDigits: a recovered frame reaches the client,
// so it must equal the upstream frame byte for byte except the index number:
// key order, spacing, "<", ">", "&", unicode and the event/data layout stay.
func TestSplicer_RewritesOnlyTheIndexDigits(t *testing.T) {
	tests := []struct {
		name string
		wire model.WireFormat
		in   string // index 0 in the recovery stream, offset 3 after the original
		want string
	}{
		{"responses key order and html", model.WireOpenAIResponses,
			"event: response.output_text.delta\ndata: {\"delta\":\"a<b>&c \\u00e9 é\",\"output_index\":0,\"type\":\"response.output_text.delta\"}\n\n",
			"event: response.output_text.delta\ndata: {\"delta\":\"a<b>&c \\u00e9 é\",\"output_index\":3,\"type\":\"response.output_text.delta\"}\n\n"},
		{"responses spacing, nested key, data only, crlf", model.WireOpenAIResponses,
			"data: { \"type\" : \"response.output_item.added\", \"item\": {\"output_index\": 9}, \"output_index\" :  1 }\r\n\r\n",
			"data: { \"type\" : \"response.output_item.added\", \"item\": {\"output_index\": 9}, \"output_index\" :  4 }\r\n\r\n"},
		{"responses digits grow", model.WireOpenAIResponses,
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\",\"output_index\":9}\n\n",
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\",\"output_index\":12}\n\n"},
		{"anthropic key order and html", model.WireAnthropicMessages,
			"event: content_block_delta\ndata: {\"delta\":{\"text\":\"1 < 2 && 3 > 2\",\"type\":\"text_delta\"},\"index\":0,\"type\":\"content_block_delta\"}\n\n",
			"event: content_block_delta\ndata: {\"delta\":{\"text\":\"1 < 2 && 3 > 2\",\"type\":\"text_delta\"},\"index\":3,\"type\":\"content_block_delta\"}\n\n"},
		{"anthropic string holding the key", model.WireAnthropicMessages,
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\\\"index\\\":9\"},\"index\":0}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\\\"index\\\":9\"},\"index\":3}\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term, _ := TerminalFor(tt.wire)
			sp := term.NewSplicer()
			seed := map[model.WireFormat][]string{
				model.WireOpenAIResponses:   {"response.output_item.added", `{"type":"response.output_item.added","output_index":2}`},
				model.WireAnthropicMessages: {"content_block_start", `{"type":"content_block_start","index":2}`},
			}[tt.wire]
			sp.Sent(seed[0], []byte(seed[1])) // the client already has items 0..2
			sp.Begin()
			raw := []byte(tt.in)
			ev, data := SplitFrame([]byte(strings.TrimRight(tt.in, "\r\n")))
			out, keep := sp.Recovery(raw, ev, data)
			if !keep || string(out) != tt.want {
				t.Fatalf("keep=%v\n got %q\nwant %q", keep, out, tt.want)
			}
		})
	}
}
