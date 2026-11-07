package forward

import (
	"bytes"

	"github.com/ethosagent/tollgate/internal/normalise"
)

// frameRec is one frame of a read, kept for the bookkeeping that follows the
// client write.
type frameRec struct {
	ev   string
	data []byte
	sent []byte // what the client was sent for it; nil if dropped or held
}

// segState splits one upstream response into frames, writes the ones before
// the terminal region and holds the rest. A frame is classified once it is
// complete: its closing blank line has arrived. An unfinished frame waits in
// pend, so the only bytes that reach the client late are the start of the
// frame being received. Not safe for concurrent use.
type segState struct {
	r        *finalRun
	recovery bool
	sum      *normalise.Summarizer
	pend     []byte     // received, not yet part of a complete frame
	held     []byte     // the terminal region so far
	out      []byte     // bytes to write for this read
	recs     []frameRec // frames of this read
	opened   bool       // the terminal region has begun
	pass     bool       // gave up: a frame passed MaxFrameBytes; relay raw
}

// frameEnd returns the length of the first complete frame of b, up to and
// including its closing blank line, or -1. Lines end in LF or CRLF; a stream
// that uses bare CR never completes a frame and is relayed raw once pend
// passes MaxFrameBytes.
func frameEnd(b []byte) int {
	for i := 0; i < len(b); {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			return -1
		}
		line := b[i : i+j]
		i += j + 1
		if len(line) == 0 || (len(line) == 1 && line[0] == '\r') {
			return i
		}
	}
	return -1
}

// feed takes one read: it writes the frames it completes that precede the
// terminal region, holds the rest, then folds every frame into the summary.
func (s *segState) feed(chunk []byte) error {
	if s.pass {
		return s.r.write(chunk)
	}
	s.pend = append(s.pend, chunk...)
	off := 0
	for n := frameEnd(s.pend[off:]); n >= 0; n = frameEnd(s.pend[off:]) {
		s.classify(s.pend[off : off+n])
		off += n
	}
	if len(s.pend)-off > MaxFrameBytes || len(s.held) > MaxFrameBytes {
		s.giveUp(off)
	}
	err := s.emit()
	s.pend = s.pend[:copy(s.pend, s.pend[off:])]
	return err
}

// classify routes one complete frame: held if the terminal region has begun
// or begins here, else queued for the client (spliced if this is a recovery
// stream).
func (s *segState) classify(raw []byte) {
	ev, data := normalise.SplitFrame(raw)
	rec := frameRec{ev: ev, data: data}
	switch {
	case s.opened:
		s.held = append(s.held, raw...)
	case s.r.f.Term.Opens(ev, data):
		s.opened = true
		s.held = append(s.held, raw...)
	default:
		out, keep := raw, true
		if s.recovery {
			out, keep = s.r.splice.Recovery(raw, ev, data)
		}
		if keep {
			s.out = append(s.out, out...)
			rec.sent = out
		}
	}
	s.recs = append(s.recs, rec)
}

// giveUp stops holding: a frame that never ends cannot be classified in
// bounded memory, so what is pending goes out and the rest is relayed raw.
// The consumer loses those frames (counted as one drop).
func (s *segState) giveUp(off int) {
	s.out = append(append(s.out, s.held...), s.pend[off:]...)
	s.held, s.pend, s.pass = nil, s.pend[:off], true
	if q := s.r.req.Frames; q != nil {
		q.lose(1)
	}
}

// emit writes the read's batch, flushed once, and only then folds its frames
// into the splicer, the consumer queue and the summary (client first).
func (s *segState) emit() error {
	defer func() { s.out, s.recs = s.out[:0], s.recs[:0] }()
	if len(s.out) > 0 {
		if err := s.r.write(s.out); err != nil {
			return err
		}
	}
	for _, f := range s.recs {
		if f.sent != nil {
			if !s.recovery {
				s.r.splice.Sent(f.ev, f.data)
			}
			if f.ev != "" || len(f.data) > 0 {
				s.r.offer(f.sent)
			}
		}
		s.sum.Frame(f.ev, f.data)
	}
	return nil
}

// end handles the bytes left when the response ends or breaks: a last frame
// without its blank line is a frame like any other.
func (s *segState) end() error {
	if s.pass || len(s.pend) == 0 {
		return nil
	}
	s.classify(s.pend)
	err := s.emit()
	s.pend = s.pend[:0]
	return err
}
