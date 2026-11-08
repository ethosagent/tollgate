package normalise

import "testing"

func TestToolOrdinals_DenseAndBounded(t *testing.T) {
	var tools toolOrdinals
	if a, b, again := tools.assign(7), tools.assign(3), tools.assign(7); a != 0 || b != 1 || again != 0 {
		t.Fatalf("assign(7,3,7) = %d,%d,%d; want 0,1,0", a, b, again)
	}
	for k := 100; len(tools.keys) < maxToolCalls; k++ {
		tools.assign(k)
	}
	// Past the cap a hostile stream's ever-new keys share one ordinal and
	// the table stops growing.
	for k := 1 << 20; k < 1<<20+10; k++ {
		if got := tools.assign(k); got != maxToolCalls {
			t.Fatalf("assign(new key past cap) = %d, want %d", got, maxToolCalls)
		}
	}
	if len(tools.keys) != maxToolCalls {
		t.Fatalf("table grew to %d keys, want %d", len(tools.keys), maxToolCalls)
	}
	if _, ok := tools.lookup(1 << 20); ok {
		t.Fatal("lookup found a key past the cap")
	}
	if ord, ok := tools.lookup(3); !ok || ord != 1 {
		t.Fatalf("lookup(3) = %d,%v; want 1,true", ord, ok)
	}
}
