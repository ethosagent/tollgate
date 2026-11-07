package forward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// Finalize configures the terminal-event barrier of one request: the
// Finalizer seam (ARCHITECTURE.md section 5, seam 8; ADR 0004). Every frame
// before the terminal region reaches the client as it arrives. The terminal
// region (normalise.Terminal.Opens) is held until Decide answers:
//
//   - release (or any Decide failure): the held bytes go out unchanged;
//   - replace: Response.Body goes out instead, verbatim;
//   - recover: one more upstream call with the same target and headers and
//     the decision's body. Its frames are spliced after the content already
//     sent (normalise.Splicer) and its own terminal region is held and
//     decided on in turn, so exactly one terminal event reaches the client.
//     If the recovery cannot start, or ends without a terminal event, the
//     original held bytes are released.
type Finalize struct {
	// Wire maps frames to the events a StreamSummary is built from.
	Wire normalise.Wire
	// Term finds the terminal region and splices recovery streams.
	Term normalise.Terminal
	// Decide is called once per held terminal region, on the relay
	// goroutine, with the summary of the response that region ends. It must
	// bound itself (recover, deadline, budgets): chain.Barrier does.
	Decide func(ctx context.Context, s model.StreamSummary) model.Decision
}

// finalizable reports whether a response can go through the barrier: a
// successful, unencoded event stream.
func finalizable(resp *http.Response) bool {
	return resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		isEventStream(resp.Header) && resp.Header.Get("Content-Encoding") == ""
}

// relayFinal relays resp through the barrier. It reports whether the response
// was started. It is a function of its own so that the closure and the flag
// it captures are heap-allocated only on this path.
func relayFinal(ctx context.Context, w http.ResponseWriter, resp *http.Response, src io.Reader, req Request, env relayEnv) (sent bool, err error) {
	begin := func() {
		sent = true
		startResponse(w, resp)
	}
	err = newFinalRun(ctx, env, w, req, begin).run(src)
	return sent, err
}

// finalRun is one client response under the barrier.
type finalRun struct {
	f      *Finalize
	ctx    context.Context
	env    relayEnv
	req    Request
	dst    http.ResponseWriter
	rc     *http.ResponseController
	begin  func()
	splice normalise.Splicer
}

func newFinalRun(ctx context.Context, env relayEnv, w http.ResponseWriter, req Request, begin func()) *finalRun {
	return &finalRun{f: req.Final, ctx: ctx, env: env, req: req, dst: w,
		rc: http.NewResponseController(w), begin: begin, splice: req.Final.Term.NewSplicer()}
}

// segResult is how one upstream response was read: its held terminal region,
// the summary of what it carried, and how reading ended.
type segResult struct {
	held []byte
	sum  model.StreamSummary
	err  error
}

// run relays src, then decides on its terminal region until a decision
// ends the response. It returns the error Tee would for the same failure.
func (r *finalRun) run(src io.Reader) error {
	cur := r.segment(src, false)
	for cur.err == nil && len(cur.held) > 0 {
		d := r.f.Decide(r.ctx, cur.sum)
		switch {
		case d.Action == model.DecisionReplace && d.Response != nil && len(d.Response.Body) > 0:
			r.offerFrames(cur.held) // hooks still see what the upstream sent
			return r.write(d.Response.Body)
		case d.Action == model.DecisionRecover && len(d.Body) > 0:
			next, ok := r.recover(d.Body)
			if !ok || (next.err == nil && len(next.held) == 0) {
				return r.release(cur)
			}
			r.offerFrames(cur.held)
			cur = next
		default:
			return r.release(cur)
		}
	}
	return r.release(cur)
}

// release sends the held terminal region and reports how the segment ended.
func (r *finalRun) release(s segResult) error {
	if len(s.held) > 0 {
		if err := r.write(s.held); err != nil {
			return err
		}
		r.offerFrames(s.held)
	}
	return s.err
}

// write sends p to the client and flushes; the first write starts the
// response.
func (r *finalRun) write(p []byte) error {
	if r.begin != nil {
		r.begin()
		r.begin = nil
	}
	if _, err := writeFlush(r.dst, r.rc, p); err != nil {
		return fmt.Errorf("%w: %w", ErrClientWrite, err)
	}
	return nil
}

// recover makes the recovery call and reads its response. ok is false when
// the call could not start (the original terminal then stands); a failure
// of the call's context is an error result instead.
func (r *finalRun) recover(body []byte) (res segResult, ok bool) {
	out, err := newUpstreamRequest(r.ctx, r.req, body)
	if err != nil {
		return res, false
	}
	out.Header = out.Header.Clone() // the transport may still hold the first request's map
	resp, err := r.env.rt.RoundTrip(out)
	if err != nil {
		if r.ctx.Err() != nil {
			return segResult{err: fmt.Errorf("%w: %w", ErrUpstreamRead, err)}, true
		}
		return res, false
	}
	defer resp.Body.Close()
	defer context.AfterFunc(r.ctx, func() { _ = resp.Body.Close() })()
	if !finalizable(resp) {
		return res, false
	}
	var src io.Reader = resp.Body
	if r.env.clock != nil {
		src = &touchReader{resp.Body, r.env.clock}
	}
	return r.segment(src, true), true
}

// offer hands one frame the client received to the consumer, if any.
func (r *finalRun) offer(sent []byte) {
	if q := r.req.Frames; q != nil {
		q.offer(bytes.TrimRight(sent, "\r\n"))
	}
}

// offerFrames offers every frame of raw, bytes the client was sent (or, for
// a discarded terminal, would have been).
func (r *finalRun) offerFrames(raw []byte) {
	for n := frameEnd(raw); n >= 0; n = frameEnd(raw) {
		if ev, data := normalise.SplitFrame(raw[:n]); ev != "" || len(data) > 0 {
			r.offer(raw[:n])
		}
		raw = raw[n:]
	}
}

// segment reads one upstream response from src. recovery marks a recovery
// stream, whose frames are spliced onto the response so far.
func (r *finalRun) segment(src io.Reader, recovery bool) segResult {
	s := &segState{r: r, recovery: recovery, sum: normalise.NewSummarizer(r.f.Wire)}
	if recovery {
		r.splice.Begin()
	}
	bp := relayBufs.Get().(*[]byte)
	defer relayBufs.Put(bp)
	for {
		n, rerr := src.Read(*bp)
		if n > 0 {
			if err := s.feed((*bp)[:n]); err != nil {
				return segResult{err: err}
			}
		}
		if rerr == nil {
			continue
		}
		if err := s.end(); err != nil {
			return segResult{err: err}
		}
		res := segResult{held: s.held, sum: s.sum.Summary()}
		if !errors.Is(rerr, io.EOF) {
			res.err = fmt.Errorf("%w: %w", ErrUpstreamRead, rerr)
		}
		return res
	}
}
