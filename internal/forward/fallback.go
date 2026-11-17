package forward

import (
	"context"
	"io"
	"net/http"
	"slices"
)

// maxRefusalDrain bounds how much of a refused response is read before it
// is closed, so the connection can be reused without a large body stalling
// the resend. The bytes are discarded: never logged, never relayed.
const maxRefusalDrain = 64 << 10

// fallback resends req.FallbackBody once when resp is a refusal in
// req.FallbackOn (ADR 0020). It runs after the round trip and before any
// byte or header goes to the client, so the refused response is dropped
// unseen. from is the refused status (0 when no fallback ran); the returned
// response is what the caller relays. A resend answered by another refusal
// is relayed as it is: there is never a second fallback.
func fallback(ctx context.Context, rt http.RoundTripper, req Request, resp *http.Response) (out *http.Response, from int, err error) {
	if req.FallbackBody == nil || !slices.Contains(req.FallbackOn, resp.StatusCode) {
		return resp, 0, nil
	}
	from = resp.StatusCode
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRefusalDrain))
	_ = resp.Body.Close()
	again, err := newUpstreamRequest(ctx, req, req.FallbackBody)
	if err != nil {
		return nil, from, err
	}
	out, err = rt.RoundTrip(again)
	return out, from, err
}
