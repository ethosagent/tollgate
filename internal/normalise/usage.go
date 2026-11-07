package normalise

import "github.com/ethosagent/tollgate/internal/model"

// TrackUsage sets *u to the last usage in evs, if any. Mappers report usage
// as a running total, so the last one seen is the call's usage. It is the one
// usage rule for streamed (EventMapper) and buffered (MapBody) responses.
func TrackUsage(u *model.Usage, evs []model.Event) {
	for _, e := range evs {
		if e.Kind == model.EventUsage && e.Usage != nil {
			*u = *e.Usage
		}
	}
}
