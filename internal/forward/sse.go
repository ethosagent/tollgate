package forward

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
// not emitted. The zero value is ready to use; a Scanner is not safe for
// concurrent use.
type Scanner struct {
	// Oversized counts frames dropped for exceeding MaxFrameBytes.
	Oversized int

	buf       []byte // current frame, each line terminated by "\n"
	lineLen   int    // bytes of the current line so far
	comment   bool   // the current line is a comment
	hasField  bool   // the frame has a non-comment line
	skipping  bool   // the frame is over the cap: discard it
	pendingCR bool   // the last byte was a CR: swallow an immediately following LF
}

// Scan consumes p and calls emit for every frame it completes. The frame
// slice is valid only during the call; copy it to keep it.
func (s *Scanner) Scan(p []byte, emit func(frame []byte)) {
	for _, b := range p {
		if s.pendingCR {
			s.pendingCR = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\r':
			s.pendingCR = true
			s.endLine(emit)
		case '\n':
			s.endLine(emit)
		default:
			s.addByte(b)
		}
	}
}

// Finish ends the stream: a last frame without its closing blank line is
// emitted. It then resets the scanner for reuse.
func (s *Scanner) Finish(emit func(frame []byte)) {
	if s.lineLen > 0 {
		s.endLine(emit)
	}
	s.endFrame(emit)
	s.Reset()
}

// Reset drops any partial frame, keeping the buffer and the Oversized count.
func (s *Scanner) Reset() {
	s.buf = s.buf[:0]
	s.lineLen, s.comment, s.hasField, s.skipping, s.pendingCR = 0, false, false, false, false
}

func (s *Scanner) addByte(b byte) {
	if s.lineLen == 0 && b == ':' {
		s.comment = true
	}
	s.lineLen++
	if s.skipping {
		return
	}
	if len(s.buf) >= MaxFrameBytes {
		s.skipping = true
		s.buf = s.buf[:0]
		return
	}
	s.buf = append(s.buf, b)
}

func (s *Scanner) endLine(emit func([]byte)) {
	if s.lineLen == 0 {
		s.endFrame(emit)
		return
	}
	if !s.comment {
		s.hasField = true
	}
	if !s.skipping {
		s.buf = append(s.buf, '\n')
	}
	s.lineLen, s.comment = 0, false
}

// endFrame runs on a blank line: emit the frame if it carries a field.
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
