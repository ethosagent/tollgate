package normalise

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func frame(event, data string) []byte {
	if event != "" {
		return []byte("event: " + event + "\ndata: " + data + "\n\n")
	}
	return []byte("data: " + data + "\n\n")
}

func TestTerminal_Opens(t *testing.T) {
	cases := []struct {
		name  string
		wire  model.WireFormat
		event string
		data  string
		want  bool
	}{
		{"openai content chunk", model.WireOpenAIChat, "", `{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`, false},
		{"openai finish chunk", model.WireOpenAIChat, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, true},
		{"openai finish chunk with spaces", model.WireOpenAIChat, "", `{"choices":[{"delta":{}, "finish_reason" : "length"}]}`, true},
		{"openai text that quotes finish_reason", model.WireOpenAIChat, "", `{"choices":[{"delta":{"content":"\"finish_reason\":\"stop\""},"finish_reason":null}]}`, false},
		{"openai done", model.WireOpenAIChat, "", "[DONE]", true},
		{"openai usage only chunk", model.WireOpenAIChat, "", `{"choices":[],"usage":{"prompt_tokens":1}}`, false},
		{"anthropic delta", model.WireAnthropicMessages, "content_block_delta", `{"type":"content_block_delta","index":0}`, false},
		{"anthropic message_delta", model.WireAnthropicMessages, "message_delta", `{"type":"message_delta"}`, true},
		{"anthropic message_stop", model.WireAnthropicMessages, "message_stop", `{"type":"message_stop"}`, true},
		{"anthropic nameless message_stop", model.WireAnthropicMessages, "", `{"type":"message_stop"}`, true},
		{"anthropic error", model.WireAnthropicMessages, "error", `{"type":"error"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term, err := TerminalFor(tc.wire)
			if err != nil {
				t.Fatal(err)
			}
			if got := term.Opens(tc.event, []byte(tc.data)); got != tc.want {
				t.Fatalf("Opens = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTerminalFor_UnsupportedWire(t *testing.T) {
	if _, err := TerminalFor(model.WirePassthrough); err == nil {
		t.Fatal("want ErrUnsupportedWire")
	}
}

func TestSplicer_OpenAIDropsRoleOnlyFirstDeltaOfEachRecovery(t *testing.T) {
	term, _ := TerminalFor(model.WireOpenAIChat)
	sp := term.NewSplicer()
	role := frame("", `{"id":"b","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	text := frame("", `{"id":"b","choices":[{"index":0,"delta":{"content":"more"},"finish_reason":null}]}`)
	for round := range 2 {
		sp.Begin()
		if out, keep := recoverFrame(sp, role); keep {
			t.Fatalf("round %d: role-only delta kept: %q", round, out)
		}
		out, keep := recoverFrame(sp, text)
		if !keep || !bytes.Equal(out, text) {
			t.Fatalf("round %d: content chunk changed: keep=%v %q", round, keep, out)
		}
		if _, keep := recoverFrame(sp, role); !keep {
			t.Fatalf("round %d: only the first frame may be dropped", round)
		}
	}
}

func TestSplicer_OpenAIKeepsFirstDeltaThatCarriesContent(t *testing.T) {
	term, _ := TerminalFor(model.WireOpenAIChat)
	sp := term.NewSplicer()
	sp.Begin()
	first := frame("", `{"choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"}}]}`)
	if out, keep := recoverFrame(sp, first); !keep || !bytes.Equal(out, first) {
		t.Fatalf("content-bearing first delta must pass unchanged, got keep=%v %q", keep, out)
	}
}

func recoverFrame(sp Splicer, raw []byte) ([]byte, bool) {
	ev, data := SplitFrame(raw)
	return sp.Recovery(raw, ev, data)
}

func TestSplicer_AnthropicOneMessageFromTwoStreams(t *testing.T) {
	term, _ := TerminalFor(model.WireAnthropicMessages)
	sp := term.NewSplicer()
	for _, f := range [][]byte{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"n"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`),
	} {
		ev, data := SplitFrame(f)
		sp.Sent(ev, data)
	}
	sp.Begin()
	for _, name := range []string{"message_start", "ping"} {
		if _, keep := recoverFrame(sp, frame(name, `{"type":"`+name+`"}`)); keep {
			t.Fatalf("%s of the recovery stream must be dropped", name)
		}
	}
	var got []string
	for _, f := range [][]byte{
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x \"index\":9"}}`),
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`),
	} {
		out, keep := recoverFrame(sp, f)
		if !keep {
			t.Fatalf("dropped %q", f)
		}
		got = append(got, string(out))
	}
	all := strings.Join(got, "")
	for _, want := range []string{`"index":2`, `"text":"x \"index\":9"`} {
		if !strings.Contains(all, want) {
			t.Errorf("rewritten frames lack %s:\n%s", want, all)
		}
	}
	if strings.Contains(all, `"index":0`) {
		t.Errorf("an index was not shifted:\n%s", all)
	}
	// A second recovery continues after the first one's last block.
	sp.Begin()
	out, _ := recoverFrame(sp, frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	if !bytes.Contains(out, []byte(`"index":3`)) {
		t.Fatalf("second recovery not offset after the first: %s", out)
	}
}

func TestSplicer_AnthropicPassesOtherFramesUnchanged(t *testing.T) {
	term, _ := TerminalFor(model.WireAnthropicMessages)
	sp := term.NewSplicer()
	sp.Begin()
	f := frame("error", `{"type":"error","error":{"message":"x"}}`)
	if out, keep := recoverFrame(sp, f); !keep || !bytes.Equal(out, f) {
		t.Fatalf("keep=%v %q", keep, out)
	}
}
