package tollgate

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
)

// TestOptions_MirrorCore fails when Options and core.Options stop having the
// same fields, in order, with the same types (Limits and HeaderNames map to
// their core copies). Limits and HeaderNames need no such test: Options.core
// converts them, which does not compile if their field lists differ.
func TestOptions_MirrorCore(t *testing.T) {
	pub, eng := reflect.TypeFor[Options](), reflect.TypeFor[core.Options]()
	if pub.NumField() != eng.NumField() {
		t.Fatalf("Options has %d fields, core.Options %d", pub.NumField(), eng.NumField())
	}
	mapped := map[reflect.Type]reflect.Type{
		reflect.TypeFor[Limits]():      reflect.TypeFor[core.Limits](),
		reflect.TypeFor[HeaderNames](): reflect.TypeFor[core.HeaderNames](),
	}
	for i := range pub.NumField() {
		p, e := pub.Field(i), eng.Field(i)
		want := p.Type
		if m, ok := mapped[want]; ok {
			want = m
		}
		if p.Name != e.Name || want != e.Type {
			t.Errorf("field %d: Options.%s %v, core.Options.%s %v", i, p.Name, p.Type, e.Name, e.Type)
		}
	}
}

// TestOptions_CoreCopiesEveryField sets every Options field and checks
// Options.core carries each one over.
func TestOptions_CoreCopiesEveryField(t *testing.T) {
	o := Options{
		Auth: NoAuth{}, Admission: Unlimited{}, Providers: noResolver{}, Sessions: noSessions{},
		Metrics: DiscardSink{}, Finalizers: []Finalizer{nil}, Transport: http.DefaultTransport,
		Logger: slog.New(slog.DiscardHandler), Limits: Limits{MaxSessions: 7}, Headers: HeaderNames{Session: "x-s"},
		MeasureSessionPrediction: true, DisablePrivacyMode: true,
	}
	c := reflect.ValueOf(o.core())
	for i := range c.NumField() {
		if c.Field(i).IsZero() {
			t.Errorf("core.Options.%s is zero: Options.core does not copy it", c.Type().Field(i).Name)
		}
	}
}

func TestDefaults_AreTheCoreDefaults(t *testing.T) {
	if DefaultLimits() != Limits(core.DefaultLimits()) || DefaultLimits() == (Limits{}) {
		t.Fatalf("DefaultLimits = %+v", DefaultLimits())
	}
	if h := DefaultHeaderNames(); h.TenantKey != "x-proxy-key" || h.Session != "x-session-id" || h.UpstreamURL != "x-upstream-url" {
		t.Fatalf("DefaultHeaderNames = %+v", h)
	}
}

func TestValidate_Delegates(t *testing.T) {
	if err := (Options{}).Validate(); err != nil {
		t.Fatalf("zero Options: %v", err)
	}
	if err := (Options{Headers: HeaderNames{TenantKey: "Authorization"}}).Validate(); err == nil {
		t.Fatal("a reserved tenant-key header was accepted")
	}
}

type noResolver struct{}

func (noResolver) Resolve(context.Context, Tenant, *http.Request) (Provider, string, error) {
	return Provider{}, "", nil
}

type noSessions struct{}

func (noSessions) Resolve(context.Context, Tenant, *Call) Session { return Session{} }
func (noSessions) Closed() <-chan Session                         { return nil }
