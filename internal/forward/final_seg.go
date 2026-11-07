package forward

import (
	"bytes"

	"github.com/ethosagent/tollgate/internal/normalise"
)

// frameRec is one frame of the read being processed, kept for the
// bookkeeping that follows the client write.
type frameRec struct {
	ev       string
	data     []byte
	off, end int  // the frame, for the consumer, in segState.arena
	sent     bool // the client was sent it in this read
}

// heldFrame is one frame of the terminal region: what the consumer gets if
// the region is released, and what the splicer must know about it.
type heldFrame struct {
	ev    string
	data  []byte
	frame []byte
}

// segState splits one upstream response into frames with the tee's Scanner
// (one framer, every line ending), writes the frames before the terminal
// region and holds the rest. A frame is classified once the Scanner reports
// its end, so only the start of the frame being received reaches the client
// late. Not safe for concurrent use.
type segState struct {
	r        *finalRun
	recovery bool
	sum      *normalise.Summarizer
	sc       Scanner

	pend []byte // raw bytes of the frame being received, from earlier reads
	cur  []byte // the bytes being framed: pend then the current read
	base int    // where the current read starts in cur
	last int    // where the next frame's raw bytes start in cur

	out   []byte     // raw bytes to write for this read
	recs  []frameRec // frames of this read
	arena []byte     // this read's frames for the consumer (Scanner frames are transient)

	held      []byte      // raw bytes of the terminal region
	heldFr    []heldFrame // its frames
	opened    bool        // the terminal region has begun
	unheld    bool        // the region outgrew MaxFrameBytes and went out undecided
	offerHeld bool        // the held frames went out in this read: offer them after the write
	partial   bool        // the start of the frame being received already went out
}

// feed takes one read: it frames it, writes the frames before the terminal
// region, holds the rest, then folds every frame into the summary.
func (s *segState) feed(chunk []byte) error {
	if s.sc.pendingCR && len(s.pend) == 0 && len(chunk) > 0 && chunk[0] == '\n' {
		// The previous read ended a frame on the CR of a CRLF: this LF is
		// the end of that frame, so it goes where that frame went.
		s.sc.pendingCR = false
		s.place(chunk[:1])
		chunk = chunk[1:]
	}
	s.cur, s.base, s.last = chunk, 0, 0
	if len(s.pend) > 0 {
		s.base = len(s.pend)
		s.pend = append(s.pend, chunk...)
		s.cur = s.pend
	}
	s.sc.scan(chunk, s)
	s.r.req.Frames.lose(s.sc.takeOversized())
	s.keep(s.cur[s.last:])
	return s.emit()
}

// frame is the Scanner's sink: the frame that ends at end (in the current
// read; -1 is the end of the stream) is cur[last:base+end].
func (s *segState) frame(f []byte, end int) {
	stop := len(s.cur)
	if end >= 0 {
		stop = s.base + end
	}
	raw := s.cur[s.last:stop]
	s.last = stop
	s.classify(raw, f)
}

// keep carries the unfinished frame to the next read. Memory stays bounded:
// past MaxFrameBytes its start goes to the client now (and a terminal
// region that would pass the cap is released undecided).
func (s *segState) keep(rest []byte) {
	if len(s.held) > MaxFrameBytes || (s.opened && len(s.held)+len(rest) > MaxFrameBytes) {
		s.unhold()
	}
	if len(rest) > MaxFrameBytes || (s.partial && len(rest) > 0) {
		s.place(rest)
		s.partial, rest = true, nil
	}
	if s.base > 0 { // cur is pend: move the rest to its front
		s.pend = s.pend[:copy(s.pend, rest)]
		return
	}
	s.pend = append(s.pend[:0], rest...)
}

// place sends raw bytes that are no frame of their own (keep-alives, the
// start of an oversized frame) where the stream is: held or out.
func (s *segState) place(raw []byte) {
	if s.opened {
		s.held = append(s.held, raw...)
	} else {
		s.out = append(s.out, raw...)
	}
}

// classify routes one frame: held if the terminal region has begun or
// begins here, else queued for the client (spliced if this is a recovery
// stream). f is nil for bytes that carry no field.
func (s *segState) classify(raw, f []byte) {
	var rec frameRec
	if f != nil { // keep the frame for after the write; data aliases this copy
		rec.off = len(s.arena)
		s.arena = append(s.arena, f...)
		rec.end = len(s.arena)
		f = s.arena[rec.off:rec.end]
		rec.ev, rec.data = normalise.SplitFrame(f)
	}
	switch {
	case f == nil: // a keep-alive, or the end of an oversized frame
		s.partial = false
		s.place(raw)
		return
	case s.partial: // its start already went out: it cannot be held or spliced
		s.partial = false
		s.unheld = s.unheld || (!s.opened && s.r.f.Term.Opens(rec.ev, rec.data))
		s.place(raw)
		rec.sent = !s.opened
	case s.opened || (!s.unheld && s.r.f.Term.Opens(rec.ev, rec.data)):
		s.opened = true
		s.held = append(s.held, raw...)
		h := heldFrame{frame: bytes.Clone(f)} // held past this read: its own copy
		h.ev, h.data = normalise.SplitFrame(h.frame)
		s.heldFr = append(s.heldFr, h)
	default:
		out, keep := raw, true
		if s.recovery {
			out, keep = s.r.splice.Recovery(raw, rec.ev, rec.data)
		}
		if !keep {
			break
		}
		s.out = append(s.out, out...)
		if len(out) != len(raw) || (len(out) > 0 && &out[0] != &raw[0]) {
			// a spliced frame: the consumer gets what the client got
			rec.off = len(s.arena)
			s.arena = append(s.arena, bytes.TrimRight(out, "\r\n")...)
			rec.end = len(s.arena)
		}
		rec.sent = true
	}
	s.recs = append(s.recs, rec)
}

// unhold gives up holding a terminal region that outgrew MaxFrameBytes: it
// goes to the client now, undecided, and no region opens again in this
// response.
func (s *segState) unhold() {
	s.out = append(s.out, s.held...)
	s.held, s.opened, s.unheld, s.offerHeld = nil, false, true, true
}

// emit writes the read's batch, flushed once, and only then folds its frames
// into the splicer, the consumer queue and the summary (client first).
func (s *segState) emit() error {
	defer func() { s.out, s.recs, s.arena = s.out[:0], s.recs[:0], s.arena[:0] }()
	if len(s.out) > 0 {
		if err := s.r.write(s.out); err != nil {
			return err
		}
	}
	if s.offerHeld {
		s.offerHeld = false
		s.r.sent(s.heldFr, s.recovery)
		s.heldFr = nil
	}
	for _, f := range s.recs {
		if f.sent {
			if !s.recovery {
				s.r.splice.Sent(f.ev, f.data)
			}
			s.r.offer(s.arena[f.off:f.end])
		}
		s.sum.Frame(f.ev, f.data)
	}
	return nil
}

// end handles the bytes left when the response ends or breaks: a last frame
// without its blank line is a frame like any other.
func (s *segState) end() error {
	s.cur, s.base, s.last = s.pend, 0, 0
	s.sc.finish(s)
	s.r.req.Frames.lose(s.sc.takeOversized())
	if s.last < len(s.cur) {
		s.classify(s.cur[s.last:], nil)
	}
	s.pend = s.pend[:0]
	return s.emit()
}
