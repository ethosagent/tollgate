package testkit

import (
	"maps"
	"slices"

	"github.com/ethosagent/tollgate"
)

// cloneCall copies c deeply enough that a plugin mutating its argument later
// cannot change the record. Credentials are left out of the header copy.
func (r *recorder) cloneCall(c *tollgate.Call) *tollgate.Call {
	if c == nil {
		return nil
	}
	out := *c
	out.Header = r.scrub(c.Header)
	out.Body = slices.Clone(c.Body)
	out.Meta = maps.Clone(c.Meta)
	out.Tenant = cloneTenant(c.Tenant)
	out.Messages = make([]tollgate.Message, len(c.Messages))
	for i, m := range c.Messages {
		m.ToolCalls = slices.Clone(m.ToolCalls)
		for j := range m.ToolCalls {
			m.ToolCalls[j].Arguments = slices.Clone(m.ToolCalls[j].Arguments)
		}
		out.Messages[i] = m
	}
	return &out
}

func cloneTenant(t tollgate.Tenant) tollgate.Tenant {
	t.Attrs = maps.Clone(t.Attrs)
	if t.Policy != nil {
		p := *t.Policy
		p.AllowedProviders = slices.Clone(p.AllowedProviders)
		p.AllowedModels = slices.Clone(p.AllowedModels)
		p.AllowedUpstreams = slices.Clone(p.AllowedUpstreams)
		p.Overrides = maps.Clone(p.Overrides)
		t.Policy = &p
	}
	return t
}

func cloneEvent(e tollgate.Event) *tollgate.Event {
	e.Raw = slices.Clone(e.Raw)
	if e.ToolCall != nil {
		tc := *e.ToolCall
		e.ToolCall = &tc
	}
	if e.Usage != nil {
		u := *e.Usage
		e.Usage = &u
	}
	return &e
}

func cloneResponse(r *tollgate.Response) *tollgate.Response {
	if r == nil {
		return nil
	}
	out := *r
	out.Header = r.Header.Clone()
	out.Body = slices.Clone(r.Body)
	return &out
}

func cloneSummary(s tollgate.StreamSummary) *tollgate.StreamSummary {
	s.ToolCalls = slices.Clone(s.ToolCalls)
	for i := range s.ToolCalls {
		s.ToolCalls[i].Arguments = slices.Clone(s.ToolCalls[i].Arguments)
	}
	return &s
}

func cloneDecision(d tollgate.Decision) *tollgate.Decision {
	d.Body = slices.Clone(d.Body)
	d.Response = cloneResponse(d.Response)
	return &d
}
