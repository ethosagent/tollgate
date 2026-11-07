package methodset

import (
	"reflect"
	"testing"
)

// base plays NoopHook: the origin whose methods count as "not implemented".
type base struct{}

func (base) M()  {}
func (base) N()  {}
func (*base) P() {}

type embedsBase struct{ base }

type valueOverride struct{ base }

func (valueOverride) M() {}

type ptrOverride struct{ base }

func (*ptrOverride) M() {}

type generic[T any] struct{ base }

func (generic[T]) M() {}

type inner struct{ base }

func (*inner) M() {}

type outer struct{ inner }

type embedsPtr struct{ *base }

type other struct{}

func (other) M() {}

// ambiguous has M from two embeds; the compiler hides it, so it is not
// provably base's. N comes from base alone.
type ambiguous struct {
	base
	other
}

var _ = ambiguous{other: other{}}

type noBase struct{}

func (noBase) M() {}

var origin = reflect.TypeFor[base]()

func TestPromotedFrom(t *testing.T) {
	tests := []struct {
		name   string
		v      any
		method string
		want   bool
	}{
		{"the origin itself", base{}, "M", true},
		{"pointer to the origin", &base{}, "M", true},
		{"embedding only", embedsBase{}, "M", true},
		{"embedding only, pointer method", &embedsBase{}, "P", true},
		{"embedded by pointer", embedsPtr{}, "M", true},
		{"value override", valueOverride{}, "M", false},
		{"value override via pointer", &valueOverride{}, "M", false},
		{"other method still promoted", valueOverride{}, "N", true},
		{"pointer override", &ptrOverride{}, "M", false},
		{"generic override", generic[int]{}, "M", false},
		{"override in an embedded type", &outer{}, "M", false},
		{"one provider of N among two embeds", ambiguous{}, "N", true},
		{"two providers of M: not provable", ambiguous{}, "M", false},
		{"no origin at all", noBase{}, "M", false},
		{"not a struct", 3, "M", false},
		{"nil", nil, "M", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PromotedFrom(reflect.TypeOf(tt.v), tt.method, origin); got != tt.want {
				t.Fatalf("PromotedFrom = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeclares(t *testing.T) {
	tests := []struct {
		name   string
		v      any
		method string
		want   bool
	}{
		{"value method", base{}, "M", true},
		{"value method via pointer", &base{}, "M", true},
		{"own value method beside an embed", valueOverride{}, "M", true},
		{"pointer receiver", &ptrOverride{}, "M", true},
		{"pointer receiver, value given", ptrOverride{}, "M", true},
		{"promoted only", embedsBase{}, "M", false},
		{"promoted only, via pointer", &embedsBase{}, "M", false},
		{"promoted from an embedded override", &outer{}, "M", false},
		{"no such method", other{}, "Z", false},
		{"nil", nil, "M", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Declares(reflect.TypeOf(tt.v), tt.method); got != tt.want {
				t.Fatalf("Declares = %v, want %v", got, tt.want)
			}
		})
	}
}
