package chain

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/ethosagent/tollgate/internal/model"
)

// cloneCall is the private copy a Before hook writes to. Isolated (deep):
// Body, Header, Tenant.Attrs, Messages with their ToolCalls and Arguments, and
// Meta, recursively through the JSON-shaped value types cloneValue knows.
// Shared (read-only by contract): Tenant.Policy, Provider.BaseURL and any
// other Meta value (pointers, structs, channels, funcs): a late hook could
// still write through those, so extensions must not put mutable state there.
func cloneCall(c *model.Call) model.Call {
	cp := *c
	cp.Body = slices.Clone(c.Body)
	cp.Header = c.Header.Clone()
	cp.Tenant.Attrs = maps.Clone(c.Tenant.Attrs)
	cp.Messages = slices.Clone(c.Messages)
	for i := range cp.Messages {
		tcs := slices.Clone(cp.Messages[i].ToolCalls)
		for j := range tcs {
			tcs[j].Arguments = slices.Clone(tcs[j].Arguments)
		}
		cp.Messages[i].ToolCalls = tcs
	}
	if c.Meta != nil {
		cp.Meta = cloneMap(c.Meta)
	}
	return cp
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

// cloneValue deep-copies the reference types a JSON-shaped Meta value is made
// of; any other value is returned as is (shared).
func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneValue(e)
		}
		return out
	case map[string]string:
		return maps.Clone(x)
	case []string:
		return slices.Clone(x)
	case []byte:
		return slices.Clone(x)
	case json.RawMessage:
		return slices.Clone(x)
	}
	return v
}

// cloneResponse is the private copy an OnResponse hook gets.
func cloneResponse(r *model.Response) *model.Response {
	return &model.Response{Status: r.Status, Header: r.Header.Clone(), Body: slices.Clone(r.Body)}
}
