package session

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// BenchmarkResolve is the per-call cost of attributing a call (HOT-7), on
// every core at once: the header path, and a predicted agent run whose
// system prompt is 10 KB (prediction hashes it on every call).
func BenchmarkResolve(b *testing.B) {
	system := strings.Repeat("You are a careful coding agent. ", 320) // ~10 KB
	steps := agentRunWith(system, 6)
	for _, header := range []bool{true, false} {
		name := map[bool]string{true: "header", false: "predicted"}[header]
		b.Run(name, func(b *testing.B) {
			s := New(Config{})
			defer s.Close()
			ids := make([]string, 256)
			for i := range ids {
				ids[i] = "run-" + strconv.Itoa(i)
			}
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					i++
					c := &model.Call{Messages: steps[i%len(steps)]}
					if header {
						c.SessionHeader = ids[i%len(ids)]
					}
					s.Resolve(context.Background(), model.Tenant{ID: "acme"}, c)
				}
			})
		})
	}
}

// agentRunWith is agentRun with a given system prompt.
func agentRunWith(system string, steps int) [][]model.Message {
	run := agentRun(steps)
	for _, msgs := range run {
		msgs[0] = sys(system)
	}
	return run
}
