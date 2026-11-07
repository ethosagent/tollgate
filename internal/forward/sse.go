package forward

import "bytes"

// MaxFrameBytes caps one SSE frame. A longer frame (a hostile or broken
// upstream that never sends a blank line) is dropped and counted in
// Scanner.Oversized, so the scanner's memory stays bounded.
const MaxFrameBytes = 1 << 20

// Scanner splits an SSE byte stream into frames. Feed it the bytes in any
// split (frames may straddle reads); it understands LF, CRLF and CR line
// endings. It is wire-agnostic: it never looks inside a frame.
//
// A frame is the lines between blank lines, with line endings normalised to
// "\n" and no trailing newline, so it is the input internal/normalise
// SplitFrame expects. Frames made only of comment lines (keep-alives) are
// not emitted. A frame that lies wholly inside one Scan input with plain LF
// endings is emitted as a subslice of that input (no copy); only frames that
// straddle reads or need CR normalising are assembled in an internal buffer.
// The zero value is ready to use; a Scanner is not safe for concurrent use.
type Scanner struct {
	// Oversized counts frames dropped for exceeding MaxFrameBytes.
	Oversized int

	buf       []byte // carried frame: normalised lines, then any partial line
	lineLen   int    // bytes of the current (partial) line so far
	comment   bool   // the current line is a comment
	hasField  bool   // the frame has a non-comment line
	skipping  bool   // the carried frame is over the cap: discard it
	pendingCR bool   // the last byte was a CR: swallow an immediately following LF
}

// Scan consumes p and calls emit for every frame it completes. The frame
// slice is valid only during the call; copy it to keep it.
func (s *Scanner) Scan(p []byte, emit func(frame []byte)) {
	if s.pendingCR {
		s.pendingCR = false
		if len(p) > 0 && p[0] == '\n' {
			p = p[1:]
		}
	}
	// in is where the uncopied part of the current frame starts in p. While
	// inPlace, p[in:pos] is the whole frame so far, already normalised.
	in, pos := 0, 0
	inPlace := len(s.buf) == 0 && s.lineLen == 0 && !s.skipping
	for pos < len(p) {
		i := eol(p[pos:])
		if i < 0 {
			break
		}
		end := pos + i
		next := s.afterEOL(p, end)
		in, inPlace = s.endLine(p, in, pos, end, next, inPlace, emit)
		pos = next
	}
	s.carry(p[in:]) // unfinished lines wait for the next read
	if pos < len(p) {
		if s.lineLen == 0 {
			s.comment = p[pos] == ':'
		}
		s.lineLen += len(p) - pos
	}
}

// endLine handles the line p[pos:end] whose ending runs to next, and returns
// the new frame start and whether the frame is still in place.
func (s *Scanner) endLine(p []byte, in, pos, end, next int, inPlace bool, emit func([]byte)) (int, bool) {
	if s.lineLen == 0 && end == pos { // blank line: the frame ends
		if inPlace {
			s.endInPlace(p[in:pos], emit)
		} else {
			s.endFrame(emit)
		}
		return next, true
	}
	s.noteLine(p[pos:end])
	if inPlace && p[end] == '\n' {
		return in, true
	}
	s.carry(p[in:end]) // move to buf, normalising the line ending
	s.carry(newline)
	return next, false
}

var newline = []byte{'\n'}

// eol is the index of the first CR or LF in p, or -1.
func eol(p []byte) int {
	i := bytes.IndexByte(p, '\n')
	if i < 0 {
		i = len(p)
	}
	if j := bytes.IndexByte(p[:i], '\r'); j >= 0 {
		return j
	}
	if i == len(p) {
		return -1
	}
	return i
}

// afterEOL returns the index after the line ending at p[end]. A CR as the
// last byte of p may be the first half of a CRLF split across reads.
func (s *Scanner) afterEOL(p []byte, end int) int {
	if p[end] == '\r' {
		if end+1 == len(p) {
			s.pendingCR = true
		} else if p[end+1] == '\n' {
			return end + 2
		}
	}
	return end + 1
}

// noteLine completes a non-blank line whose tail (possibly all of it) is
// rest; a head may already be carried.
func (s *Scanner) noteLine(rest []byte) {
	if s.lineLen == 0 {
		s.comment = rest[0] == ':'
	}
	if !s.comment {
		s.hasField = true
	}
	s.lineLen, s.comment = 0, false
}

// carry appends b to the carried frame unless that would pass the cap.
func (s *Scanner) carry(b []byte) {
	if s.skipping || len(b) == 0 {
		return
	}
	if len(s.buf)+len(b) > MaxFrameBytes {
		s.skipping, s.buf = true, s.buf[:0]
		return
	}
	s.buf = append(s.buf, b...)
}

// endInPlace ends a frame that lies wholly in the current input; frame ends
// with the "\n" of its last line (or is empty).
func (s *Scanner) endInPlace(frame []byte, emit func([]byte)) {
	switch {
	case len(frame) > MaxFrameBytes:
		s.Oversized++
	case s.hasField && len(frame) > 0:
		emit(frame[:len(frame)-1])
	}
	s.hasField = false
}

// endFrame ends the carried frame: emit it if it carries a field.
func (s *Scanner) endFrame(emit func([]byte)) {
	switch {
	case s.skipping:
		s.Oversized++
	case s.hasField && len(s.buf) > 0:
		emit(s.buf[:len(s.buf)-1])
	}
	s.buf = s.buf[:0]
	s.hasField, s.skipping = false, false
}

// Finish ends the stream: a last frame without its closing blank line is
// emitted. The frame state is then reset for reuse; Oversized is kept.
func (s *Scanner) Finish(emit func(frame []byte)) {
	if s.lineLen > 0 {
		s.noteLine(nil)
		s.carry(newline)
	}
	s.endFrame(emit)
	s.Reset()
}

// Reset drops any partial frame, keeping the buffer and the Oversized count.
func (s *Scanner) Reset() {
	s.buf = s.buf[:0]
	s.lineLen, s.comment, s.hasField, s.skipping, s.pendingCR = 0, false, false, false, false
}

// takeOversized returns Oversized and zeroes it, so the tee can fold drops
// into the queue's gap count as they happen.
func (s *Scanner) takeOversized() int64 {
	n := s.Oversized
	s.Oversized = 0
	return int64(n)
}
