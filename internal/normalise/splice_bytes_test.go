package normalise

import (
	"encoding/json"
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

// TestSplicer_FallsBackToReserialisingWhenTheIndexIsNotPatchable: when the
// digits cannot be patched in place (data over several lines, an escaped key,
// a negative or very long value) the index must still be shifted: a duplicate
// index would merge two items for the client, which is worse than reordered keys.
func TestSplicer_FallsBackToReserialisingWhenTheIndexIsNotPatchable(t *testing.T) {
	tests := []struct {
		name  string
		wire  model.WireFormat
		in    string
		key   string
		start int // the member's value in the recovery stream
	}{
		{"responses multi data line", model.WireOpenAIResponses,
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\ndata: \"output_index\":0}\n\n", "output_index", 0},
		{"responses escaped key", model.WireOpenAIResponses,
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output\\u005findex\":0}\n\n", "output_index", 0},
		{"responses negative", model.WireOpenAIResponses,
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":-1}\n\n", "output_index", -1},
		{"responses ten digits", model.WireOpenAIResponses,
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":1234567890}\n\n", "output_index", 1234567890},
		{"anthropic multi data line", model.WireAnthropicMessages,
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\ndata: \"index\":0}\n\n", "index", 0},
		{"anthropic escaped key", model.WireAnthropicMessages,
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"ind\\u0065x\":0}\n\n", "index", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term, _ := TerminalFor(tt.wire)
			sp := term.NewSplicer()
			sp.Sent(map[model.WireFormat]string{model.WireOpenAIResponses: "response.output_item.added", model.WireAnthropicMessages: "content_block_start"}[tt.wire],
				[]byte(map[model.WireFormat]string{model.WireOpenAIResponses: `{"output_index":2}`, model.WireAnthropicMessages: `{"index":2}`}[tt.wire]))
			sp.Begin()
			raw := []byte(tt.in)
			ev, data := SplitFrame([]byte(strings.TrimRight(tt.in, "\r\n")))
			out, keep := sp.Recovery(raw, ev, data)
			if !keep {
				t.Fatal("frame dropped")
			}
			ev2, data2 := SplitFrame([]byte(strings.TrimRight(string(out), "\r\n")))
			dec := json.NewDecoder(strings.NewReader(string(data2)))
			dec.UseNumber()
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("out %q is not JSON: %v", out, err)
			}
			got, ok := doc[tt.key].(json.Number)
			want := int64(tt.start) + 3
			if n, err := got.Int64(); !ok || err != nil || n != want {
				t.Fatalf("%s = %v in %q, want %d (event %q)", tt.key, doc[tt.key], out, want, ev2)
			}
		})
	}
}

// TestResponsesSplicer_DataOnlyFramesWithSpacing: without event lines the
// splicer reads the JSON type, whatever spacing a gateway added, so a second
// response.created never reaches the client and a spaced output_item.added
// still moves the next index.
func TestResponsesSplicer_DataOnlyFramesWithSpacing(t *testing.T) {
	term, _ := TerminalFor(model.WireOpenAIResponses)
	sp := term.NewSplicer()
	sp.Sent("", []byte(`{ "type" : "response.output_item.added", "output_index" : 4 }`))
	sp.Begin()
	for _, d := range []string{
		`{ "type" : "response.created", "response" : {} }`,
		"{\n  \"response\": {},\n  \"type\": \"response.in_progress\"\n}",
	} {
		raw := []byte("data: " + strings.ReplaceAll(d, "\n", " ") + "\n\n")
		_, data := SplitFrame(raw)
		if _, keep := sp.Recovery(raw, "", data); keep {
			t.Fatalf("recovery frame %q was let through", d)
		}
	}
	raw := []byte(`data: { "output_index" : 0 , "type" : "response.output_item.added" }` + "\n\n")
	_, data := SplitFrame(raw)
	out, keep := sp.Recovery(raw, "", data)
	if !keep || string(out) != `data: { "output_index" : 5 , "type" : "response.output_item.added" }`+"\n\n" {
		t.Fatalf("keep=%v out=%q, want index 5 (after the 5 items already sent)", keep, out)
	}
	sp.Begin()
	raw = []byte(`data: {"type":"response.output_text.delta","output_index":0}` + "\n\n")
	_, data = SplitFrame(raw)
	if out, _ := sp.Recovery(raw, "", data); !strings.Contains(string(out), `"output_index":6`) {
		t.Fatalf("second recovery = %q, want index 6: the spaced added frame must count", out)
	}
}
