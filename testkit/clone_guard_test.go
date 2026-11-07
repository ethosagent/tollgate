package testkit

import (
	"reflect"
	"slices"
	"testing"

	"github.com/ethosagent/tollgate"
)

// referenceFields lists the fields of t that share memory when the struct is
// copied: slices, maps, pointers, interfaces, channels and funcs.
func referenceFields(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		switch t.Field(i).Type.Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
			out = append(out, t.Field(i).Name)
		}
	}
	return out
}

// TestClones_CoverEveryReferenceField fails when a recorded type gains a
// field that a plain struct copy would share with the plugin's argument:
// copy.go must clone it (then add it here), or the Recording could change
// after the plugin returns.
func TestClones_CoverEveryReferenceField(t *testing.T) {
	handled := map[reflect.Type][]string{
		reflect.TypeFor[tollgate.Call]():          {"Header", "Body", "Meta", "Messages"},
		reflect.TypeFor[tollgate.Message]():       {"ToolCalls"},
		reflect.TypeFor[tollgate.ToolCall]():      {"Arguments"},
		reflect.TypeFor[tollgate.Tenant]():        {"Attrs", "Policy"},
		reflect.TypeFor[tollgate.Policy]():        {"AllowedProviders", "AllowedModels", "AllowedUpstreams", "Overrides", "PrivacyMode"},
		reflect.TypeFor[tollgate.Event]():         {"Raw", "ToolCall", "Usage"},
		reflect.TypeFor[tollgate.Response]():      {"Header", "Body"},
		reflect.TypeFor[tollgate.StreamSummary](): {"ToolCalls"},
		reflect.TypeFor[tollgate.Decision]():      {"Body", "Response"},
	}
	for typ, want := range handled {
		got := referenceFields(typ)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%v reference fields = %v, copy.go clones %v: clone the new field and list it here", typ, got, want)
		}
	}
}

func TestCloneTenant_DoesNotSharePrivacyMode(t *testing.T) {
	on := true
	orig := tollgate.Tenant{Policy: &tollgate.Policy{PrivacyMode: &on}}
	c := cloneTenant(orig)
	on = false
	if !*c.Policy.PrivacyMode {
		t.Fatal("changing the plugin's PrivacyMode changed the recorded tenant")
	}
}
