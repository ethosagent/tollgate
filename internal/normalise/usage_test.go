package normalise

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func TestTrackUsage_KeepsTheLastRunningTotal(t *testing.T) {
	u := model.Usage{InputTokens: 1}
	TrackUsage(&u, []model.Event{{Kind: model.EventTextDelta}, {Kind: model.EventUsage}}) // nil Usage ignored
	if u.InputTokens != 1 {
		t.Fatalf("u = %+v, want unchanged", u)
	}
	TrackUsage(&u, []model.Event{
		{Kind: model.EventUsage, Usage: &model.Usage{InputTokens: 5}},
		{Kind: model.EventUsage, Usage: &model.Usage{InputTokens: 5, OutputTokens: 9}},
	})
	if u != (model.Usage{InputTokens: 5, OutputTokens: 9}) {
		t.Fatalf("u = %+v, want the last total", u)
	}
}
