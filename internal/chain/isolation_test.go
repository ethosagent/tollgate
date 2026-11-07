package chain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// richCall has state at every depth a hook could reach through *Call.
func richCall() *model.Call {
	return &model.Call{
		ID: "call-1", Tenant: model.Tenant{ID: "acme", Attrs: map[string]string{"tier": "gold"}},
		Header: http.Header{"X-A": {"1"}}, Body: []byte(`{"a":1}`),
		Messages: []model.Message{{Role: model.RoleAssistant, Text: "hi",
			ToolCalls: []model.ToolCall{{ID: "t1", Name: "f", Arguments: json.RawMessage(`{"x":1}`)}}}},
		Meta: map[string]any{
			"m": map[string]any{"k": "v", "l": []any{"a"}}, "s": []string{"a"}, "b": []byte("b"),
			"ms": map[string]string{"k": "v"}, "raw": json.RawMessage(`1`),
		},
	}
}

// scribble writes through every reference a *Call holds.
func scribble(c *model.Call) {
	c.Tenant.Attrs["tier"] = "x"
	c.Header["X-A"][0] = "x"
	c.Body[0] = 'x'
	c.Messages[0].Text = "x"
	c.Messages[0].ToolCalls[0].Name = "x"
	c.Messages[0].ToolCalls[0].Arguments[0] = 'x'
	m := c.Meta["m"].(map[string]any)
	m["k"] = "x"
	m["l"].([]any)[0] = "x"
	c.Meta["s"].([]string)[0] = "x"
	c.Meta["b"].([]byte)[0] = 'x'
	c.Meta["ms"].(map[string]string)["k"] = "x"
	c.Meta["raw"].(json.RawMessage)[0] = 'x'
	c.Meta["new"] = 1
}

func TestBefore_FailedHookCannotMutateAdoptedStateAtAnyDepth(t *testing.T) {
	tests := []struct {
		name   string
		before func(context.Context, *model.Call) (*model.Response, error)
	}{
		{"error", func(_ context.Context, c *model.Call) (*model.Response, error) {
			scribble(c)
			return nil, errors.New("no")
		}},
		{"panic", func(_ context.Context, c *model.Call) (*model.Response, error) {
			scribble(c)
			panic("x")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, _ := newLogger()
			call := richCall()
			New([]model.Hook{&hook{name: "bad", before: tt.before}}, log, testLimits).Before(context.Background(), call)
			if !reflect.DeepEqual(call, richCall()) {
				t.Fatalf("call changed by a failed hook:\n%+v", call)
			}
		})
	}
}

func TestBefore_LateHookCannotMutateAdoptedStateAtAnyDepth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, _ := newLogger()
		late := &hook{name: "late", before: func(ctx context.Context, c *model.Call) (*model.Response, error) {
			<-ctx.Done() // misses its deadline, then writes everywhere
			scribble(c)
			return nil, nil
		}}
		call := richCall()
		New([]model.Hook{late, &hook{name: "ok"}}, log, testLimits).Before(context.Background(), call)
		synctest.Wait() // the late hook has finished scribbling on its copy
		if !reflect.DeepEqual(call, richCall()) {
			t.Fatalf("call changed by a late hook:\n%+v", call)
		}
	})
}

func TestOnResponse_FailedHookCannotMutateTheResponse(t *testing.T) {
	log, _ := newLogger()
	in := &model.Response{Status: 200, Header: http.Header{"X-A": {"1"}}, Body: []byte("abc")}
	bad := &hook{name: "bad", onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
		r.Header["X-A"][0], r.Body[0] = "x", 'x'
		return r, errors.New("no")
	}}
	out := New([]model.Hook{bad}, log, testLimits).OnResponse(context.Background(), newCall("x"), in)
	if out != in || in.Header.Get("X-A") != "1" || string(in.Body) != "abc" {
		t.Fatalf("out=%+v in=%+v, want the untouched input", out, in)
	}
}

func TestStage_LogsClientGoneApartFromDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		slow := func(ctx context.Context, _ *model.Call) (*model.Response, error) { <-ctx.Done(); return nil, nil }
		hooks := []model.Hook{&hook{name: "a", before: slow}, &hook{name: "b", before: slow}, &hook{name: "c", before: slow}, &hook{name: "d", before: slow}, &hook{name: "e", before: slow}}

		log, logs := newLogger()
		lim := Limits{Hook: 50 * time.Millisecond, Total: 175 * time.Millisecond} // ends inside hook d
		New(hooks, log, lim).Before(context.Background(), newCall("x"))
		if g := logs.String(); !strings.Contains(g, "level=ERROR msg=\"hook skipped\" hook=e method=before call_id=call-1 tenant=acme reason=chain_deadline") {
			t.Fatalf("deadline log = %q", g)
		}

		log, logs = newLogger()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		New(hooks, log, testLimits).Before(ctx, newCall("x"))
		g := logs.String()
		if strings.Contains(g, "chain_deadline") || strings.Contains(g, "level=ERROR") || !strings.Contains(g, "level=INFO msg=\"hook skipped\" hook=a method=before call_id=call-1 tenant=acme reason=client_gone") {
			t.Fatalf("client-gone log = %q", g)
		}
	})
}
