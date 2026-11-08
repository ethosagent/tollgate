package core_test

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

const responsesPath = "/openai/v1/responses"

// TestResponses_BeforeHookSeesTheNormalisedCall: POST /openai/v1/responses is
// normalised, not passed through (Plan 08 / ADR 0018).
func TestResponses_BeforeHookSeesTheNormalisedCall(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	seen := make(chan model.Call, 1)
	h := &tHook{name: "obs", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
		seen <- model.Call{Provider: c.Provider, Model: c.Model, Stream: c.Stream, Messages: c.Messages, Meta: c.Meta}
		return nil, nil
	}}
	base, _ := newHookedProxy(t, core.Options{}, h)
	body := fixtureOrFail(t, "openai/request-responses-tools")
	readAll(t, post(t, base, responsesPath, body, via(up, fixtureHeader, "openai/responses-stream-tools")))
	c := <-seen
	if c.Provider.Wire != model.WireOpenAIResponses || c.Model != "o4-mini" || !c.Stream || len(c.Messages) != 5 {
		t.Fatalf("call = wire %q model %q stream %v, %d messages", c.Provider.Wire, c.Model, c.Stream, len(c.Messages))
	}
	if tc := c.Messages[2].ToolCalls; len(tc) != 1 || tc[0].Name != "get_weather" || c.Messages[3].ToolCallID != "call_fixture002" {
		t.Fatalf("tool turn not normalised: %+v", c.Messages)
	}
	if c.Meta[model.MetaPreviousResponseID] != "resp_fixture000" || c.Meta[model.MetaReasoningEffort] != "medium" {
		t.Fatalf("meta = %v", c.Meta)
	}
}

func TestResponses_FinalizerSeesTheStreamSummary(t *testing.T) {
	const name = "openai/responses-stream-tools"
	up := newFakeUpstream(t, upstreamOptions{})
	var got model.StreamSummary
	f := &finalizer{fn: func(_ int, s model.StreamSummary) model.Decision { got = s; return model.Decision{} }}
	readAll(t, post(t, finalizerProxy(t, core.Options{}, f), pathFor(name), requestBody(t, name), via(up, fixtureHeader, name)))
	if got.FinishReason != "completed" || got.Usage.InputTokens != 40 || got.Usage.OutputTokens != 11 || got.Events == 0 ||
		len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "get_weather" || got.ToolCalls[0].ID != "call_fixture001" ||
		string(got.ToolCalls[0].Arguments) != `{"city":"Paris"}` {
		t.Fatalf("summary = %+v (args %s)", got, got.ToolCalls)
	}
}

// TestResponses_MetricUsageStreamedAndBuffered: usage reaches the metric from
// a Responses stream and from a stream:false body.
func TestResponses_MetricUsageStreamedAndBuffered(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		stream  bool
		want    model.Usage
	}{
		{"openai/responses-stream-reasoning", true, model.Usage{InputTokens: 12, OutputTokens: 9, ReasoningTokens: 6}},
		{"openai/responses", false, model.Usage{InputTokens: 12, OutputTokens: 2}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			up := newFakeUpstream(t, upstreamOptions{})
			done := make(chan model.Metric, 1)
			h := &tHook{name: "m", after: func(_ context.Context, _ *model.Call, m model.Metric) { done <- m }}
			base, _ := newHookedProxy(t, core.Options{}, h)
			body := fixtures.Request(tc.fixture)
			readAll(t, post(t, base, responsesPath, body, via(up, fixtureHeader, tc.fixture)))
			if m := <-done; m.Usage != tc.want || m.Stream != tc.stream || m.Status != http.StatusOK {
				t.Fatalf("metric = %+v, want usage %+v stream %v", m, tc.want, tc.stream)
			}
		})
	}
}

// wireResolver sends every call to up with a fixed wire, whatever the path.
type wireResolver struct {
	up   *fakeUpstream
	wire model.WireFormat
}

func (r wireResolver) Resolve(_ context.Context, _ model.Tenant, req *http.Request) (model.Provider, string, error) {
	u, _ := url.Parse(r.up.URL)
	return model.Provider{Name: "custom", Wire: r.wire, BaseURL: u}, strings.TrimPrefix(req.URL.Path, "/custom"), nil
}

// TestResponses_WireChosenForEveryResolver: the core picks the Responses wire
// for a custom resolver's OpenAI-compatible provider, and leaves other wires
// alone (ADR 0018).
func TestResponses_WireChosenForEveryResolver(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wire     model.WireFormat
		path     string
		wantWire model.WireFormat
		parsed   bool // the body was normalised into Messages
	}{
		{"chat provider on /v1/responses", model.WireOpenAIChat, "/custom/v1/responses", model.WireOpenAIResponses, true},
		{"chat provider on chat path", model.WireOpenAIChat, "/custom/v1/chat/completions", model.WireOpenAIChat, false},
		{"passthrough stays passthrough", model.WirePassthrough, "/custom/v1/responses", model.WirePassthrough, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := newFakeUpstream(t, upstreamOptions{})
			got := make(chan model.Call, 1)
			h := &tHook{name: "obs", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
				got <- model.Call{Provider: c.Provider, Messages: c.Messages}
				return nil, nil
			}}
			base, _ := newHookedProxy(t, core.Options{Providers: wireResolver{up, tc.wire}}, h)
			readAll(t, post(t, base, tc.path, []byte(`{"model":"m","input":"hi"}`), via(up, fixtureHeader, "openai/responses")))
			c := <-got
			if c.Provider.Wire != tc.wantWire || (len(c.Messages) > 0) != tc.parsed {
				t.Fatalf("wire %q with %d messages, want %q parsed=%v", c.Provider.Wire, len(c.Messages), tc.wantWire, tc.parsed)
			}
			if tc.wantWire == model.WireOpenAIResponses && !reflect.DeepEqual(c.Messages, []model.Message{{Role: model.RoleUser, Text: "hi"}}) {
				t.Fatalf("messages = %+v", c.Messages)
			}
		})
	}
}
