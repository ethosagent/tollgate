package normalise

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func TestMapFrame_Malformed(t *testing.T) {
	tests := []struct {
		wire model.WireFormat
		name string
		data string
	}{
		{model.WireOpenAIChat, "truncated json", `{"choices":[`},
		{model.WireOpenAIChat, "not json", `hello`},
		{model.WireOpenAIChat, "wrong field type", `{"choices":"x"}`},
		{model.WireAnthropicMessages, "truncated json", `{"type":"content_block_delta",`},
		{model.WireAnthropicMessages, "not json", `hello`},
		{model.WireAnthropicMessages, "wrong field type", `{"type":5}`},
		{model.WireAnthropicMessages, "done sentinel is not json", `[DONE]`},
	}
	for _, tc := range tests {
		t.Run(string(tc.wire)+"/"+tc.name, func(t *testing.T) {
			w, _ := ForWire(tc.wire)
			if evs, err := w.NewEventMapper().Frame("", []byte(tc.data)); err == nil {
				t.Fatalf("want error, got events %+v", evs)
			}
		})
	}
}

func TestMapFrame_UnknownAndBlankAreSkipped(t *testing.T) {
	tests := []struct {
		wire model.WireFormat
		name string
		data string
	}{
		{model.WireOpenAIChat, "blank", ``},
		{model.WireOpenAIChat, "done sentinel", `[DONE]`},
		{model.WireOpenAIChat, "empty object", `{}`},
		{model.WireAnthropicMessages, "blank", ``},
		{model.WireAnthropicMessages, "ping", `{"type":"ping"}`},
		{model.WireAnthropicMessages, "unknown type", `{"type":"future_event","x":1}`},
		{model.WireAnthropicMessages, "signature delta",
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta"}}`},
	}
	for _, tc := range tests {
		t.Run(string(tc.wire)+"/"+tc.name, func(t *testing.T) {
			w, _ := ForWire(tc.wire)
			evs, err := w.NewEventMapper().Frame("", []byte(tc.data))
			if err != nil || len(evs) != 0 {
				t.Fatalf("got events %+v err %v, want none", evs, err)
			}
		})
	}
}

func TestMapBody_Malformed(t *testing.T) {
	for _, wire := range []model.WireFormat{model.WireOpenAIChat, model.WireAnthropicMessages} {
		w, _ := ForWire(wire)
		for name, body := range map[string]string{
			"empty":          ``,
			"whitespace":     " \n",
			"truncated json": `{"choices":[`,
			"not json":       `nope`,
			"array body":     `[1,2]`,
		} {
			t.Run(string(wire)+"/"+name, func(t *testing.T) {
				if evs, err := w.MapBody([]byte(body)); err == nil {
					t.Fatalf("want error, got events %+v", evs)
				}
			})
		}
	}
}
