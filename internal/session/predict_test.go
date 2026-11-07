package session

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func sys(s string) model.Message  { return model.Message{Role: model.RoleSystem, Text: s} }
func user(s string) model.Message { return model.Message{Role: model.RoleUser, Text: s} }
func tool(id string) model.Message {
	return model.Message{Role: model.RoleTool, ToolCallID: id, Text: "result"}
}
func asst(text string, calls ...string) model.Message {
	m := model.Message{Role: model.RoleAssistant, Text: text}
	for _, id := range calls {
		m.ToolCalls = append(m.ToolCalls, model.ToolCall{ID: id, Name: "f"})
	}
	return m
}

func TestPrefixHash_DependsOnTenantSystemAndFirstUserOnly(t *testing.T) {
	base := []model.Message{sys("S"), user("U")}
	grown := []model.Message{sys("S"), user("U"), asst("", "c1"), tool("c1")}
	tests := []struct {
		name       string
		ta, tb     string
		a, b       []model.Message
		wantSameAs bool
	}{
		{"later turns do not matter", "t", "t", base, grown, true},
		{"other tenant", "t1", "t2", base, base, false},
		{"other system prompt", "t", "t", base, []model.Message{sys("S2"), user("U")}, false},
		{"other first user message", "t", "t", base, []model.Message{sys("S"), user("U2")}, false},
		{"field boundaries are not ambiguous", "t", "t", []model.Message{sys("ab"), user("c")}, []model.Message{sys("a"), user("bc")}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prefixHash(tc.ta, tc.a) == prefixHash(tc.tb, tc.b); got != tc.wantSameAs {
				t.Fatalf("same hash = %v, want %v", got, tc.wantSameAs)
			}
		})
	}
	if h := prefixHash("t", base); len(h) != 32 {
		t.Fatalf("hash %q has %d chars, want 32 hex chars (16 bytes)", h, len(h))
	}
}

func TestContinues_Rule(t *testing.T) {
	step1 := []model.Message{sys("S"), user("U")}
	step2 := append(append([]model.Message{}, step1...), asst("", "c1"), tool("c1"))
	step3 := append(append([]model.Message{}, step2...), asst("", "c2"), tool("c2"))
	parallel := append(append([]model.Message{}, step1...), asst("", "a", "b"), tool("a"), tool("b"))
	tests := []struct {
		name string
		prev int // turns of the previous call
		next []model.Message
		want bool
	}{
		{"agent step 1 to 2", turns(step1), step2, true},
		{"agent step 2 to 3", turns(step2), step3, true},
		{"parallel tool results count as one turn", turns(step1), parallel, true},
		{"retry of the same request", turns(step2), step2, true},
		{"grew by more than two", turns(step1), step3, false},
		{"history shrank", turns(step3), step2, false},
		{"last assistant turn had no tool call", turns(step1), []model.Message{sys("S"), user("U"), asst("hello"), user("again")}, false},
		{"no assistant turn at all", turns(step1), step1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := continues(tc.prev, tc.next); got != tc.want {
				t.Fatalf("continues(%d, %d msgs) = %v, want %v", tc.prev, len(tc.next), got, tc.want)
			}
		})
	}
}
