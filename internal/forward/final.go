package forward

import (
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

// Finalized is what the barrier did with one call's response.
type Finalized struct {
	// Usage is summed over every upstream response of the call: the
	// original and each recovery, including a terminal region the client
	// never received (the provider still charged for it).
	Usage model.Usage
	// Unheld means a terminal region outgrew MaxFrameBytes and went to the
	// client without a Decide.
	Unheld bool
}

// relayFinal relays resp through the barrier. It reports whether the response
// was started. It is a function of its own so that the closure and the flag
// it captures are heap-allocated only on this path.
func relayFinal(ctx context.Context, w http.ResponseWriter, resp *http.Response, src io.Reader, req Request, env relayEnv) (sent bool, fin *Finalized, err error) {
	begin := func() {
		sent = true
		startResponse(w, resp)
	}
	r := newFinalRun(ctx, env, w, req, begin)
	err = r.run(src)
	return sent, &r.fin, err
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
	fin    Finalized
}

func newFinalRun(ctx context.Context, env relayEnv, w http.ResponseWriter, req Request, begin func()) *finalRun {
	return &finalRun{f: req.Final, ctx: ctx, env: env, req: req, dst: w,
		rc: http.NewResponseController(w), begin: begin, splice: req.Final.Term.NewSplicer()}
}

// segResult is how one upstream response was read: its held terminal region
// (raw bytes and frames), the summary of what it carried, and how reading
// ended.
type segResult struct {
	held   []byte
	heldFr []heldFrame
	sum    model.StreamSummary
	err    error
}

// run relays src, then decides on its terminal region until a decision
// ends the response. It returns the error Tee would for the same failure.
// The consumer (Request.Frames) gets exactly the frames the client got: a
// discarded terminal region never, a replacement's frames instead.
func (r *finalRun) run(src io.Reader) error {
	cur := r.segment(src, false)
	for cur.err == nil && len(cur.held) > 0 {
		d := r.f.Decide(r.ctx, cur.sum)
		switch {
		case d.Action == model.DecisionReplace && d.Response != nil && len(d.Response.Body) > 0:
			if err := r.write(d.Response.Body); err != nil {
				return err
			}
			r.offerAll(d.Response.Body)
			return nil
		case d.Action == model.DecisionRecover && len(d.Body) > 0:
			next, ok := r.recover(d.Body)
			if !ok || (next.err == nil && len(next.held) == 0) {
				return r.release(cur)
			}
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
		r.sent(s.heldFr, false)
	}
	return s.err
}

// sent folds frames of a terminal region the client was just sent into the
// splicer (original stream only) and the consumer queue.
func (r *finalRun) sent(frames []heldFrame, recovery bool) {
	for _, f := range frames {
		if !recovery {
			r.splice.Sent(f.ev, f.data)
		}
		r.offer(f.frame)
	}
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
func (r *finalRun) offer(frame []byte) {
	if q := r.req.Frames; q != nil {
		q.offer(frame)
	}
}

// offerAll hands the consumer the frames of raw, bytes the client was sent
// whole (a Finalizer's replacement), framed as the tee frames.
func (r *finalRun) offerAll(raw []byte) {
	if q := r.req.Frames; q != nil {
		var sc Scanner
		sc.scan(raw, q)
		sc.finish(q)
		q.lose(sc.takeOversized())
	}
}

// segment reads one upstream response from src. recovery marks a recovery
// stream, whose frames are spliced onto the response so far. The response's
// usage is added to the call's, whatever happens to its terminal region.
func (r *finalRun) segment(src io.Reader, recovery bool) (res segResult) {
	s := &segState{r: r, recovery: recovery, sum: normalise.NewSummarizer(r.f.Wire)}
	defer func() {
		res.sum = s.sum.Summary()
		r.fin.Usage = addUsage(r.fin.Usage, res.sum.Usage)
		r.fin.Unheld = r.fin.Unheld || s.unheld
	}()
	if recovery {
		r.splice.Begin()
	}
	rb := newRelayBuf()
	defer rb.release()
	for {
		buf := rb.bytes()
		n, rerr := src.Read(buf)
		if n > 0 {
			if err := s.feed(buf[:n]); err != nil {
				return segResult{err: err}
			}
		}
		if rerr == nil {
			rb.read(n) // fed: feed copies what it keeps
			continue
		}
		if err := s.end(); err != nil {
			return segResult{err: err}
		}
		res = segResult{held: s.held, heldFr: s.heldFr}
		if !errors.Is(rerr, io.EOF) {
			res.err = fmt.Errorf("%w: %w", ErrUpstreamRead, rerr)
		}
		return res
	}
}

func addUsage(a, b model.Usage) model.Usage {
	return model.Usage{InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens,
		CachedInputTokens: a.CachedInputTokens + b.CachedInputTokens, ReasoningTokens: a.ReasoningTokens + b.ReasoningTokens}
}
