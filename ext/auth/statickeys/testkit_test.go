package statickeys_test

import (
	"net/http"
	"testing"

	"github.com/ethosagent/tollgate/ext/auth/statickeys"
	"github.com/ethosagent/tollgate/testkit"
)

// TestAuth_UnderTestkitRun is the example extension test: statickeys is
// driven through a real proxy and a fake upstream by testkit.Run alone, with
// no httptest or HTTP client of its own.
func TestAuth_UnderTestkitRun(t *testing.T) {
	auth, err := statickeys.New(statickeys.Config{
		Header: "X-Team-Key",
		Keys:   map[string]statickeys.Tenant{"tenant-secret": {ID: "acme"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		key        string
		wantTenant string
		wantStatus int
	}{
		{"a known key resolves its tenant and reaches the upstream", "tenant-secret", "acme", http.StatusOK},
		{"an unknown key is refused before the upstream", "nope", "", http.StatusForbidden},
		{"a missing key is unauthorised", "", "", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr := http.Header{}
			if tt.key != "" {
				hdr.Set("X-Team-Key", tt.key)
			}
			rec := testkit.RunWith(t, testkit.Config{Plugins: []any{auth}, Fixtures: []string{"openai/chat"}, Headers: hdr})

			got := rec.Of(testkit.StageAuthenticate)
			if len(got) != 1 {
				t.Fatalf("Authenticate calls = %d, want 1", len(got))
			}
			if got[0].Request.Header.Get("X-Team-Key") != "" {
				t.Fatal("the recording kept the tenant key; credentials must be left out")
			}
			if tt.wantTenant != "" && (got[0].Tenant == nil || got[0].Tenant.ID != tt.wantTenant) {
				t.Fatalf("tenant = %+v, want %q", got[0].Tenant, tt.wantTenant)
			}
			if rec.Replies[0].Status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Replies[0].Status, tt.wantStatus)
			}
		})
	}
}
