package session

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sync"

	"github.com/ethosagent/tollgate/internal/model"
)

// maxHeaderID bounds a caller-supplied session id, so a hostile header
// cannot bloat the store or a metric label.
const maxHeaderID = 256

// validHeaderID reports whether a caller-supplied id is usable: non-empty,
// bounded and free of control characters. An unusable id is ignored and the
// call is predicted instead.
func validHeaderID(id string) bool {
	if id == "" || len(id) > maxHeaderID {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] == 0x7f {
			return false
		}
	}
	return true
}

// prefixHash identifies the start of a conversation: the first 16 bytes of
// SHA-256 over the tenant, the system prompt(s) and the first user message,
// hex encoded. Fields are NUL-separated so ("ab","c") and ("a","bc") differ.
// Turns after the first user message do not matter, so every call of an
// agent run hashes the same.
func prefixHash(tenant string, msgs []model.Message) string {
	h := sha256.New()
	bp := hashBufs.Get().(*[]byte)
	defer hashBufs.Put(bp)
	w := chunkWriter{h: h, buf: *bp}
	w.write(tenant)
	for _, m := range msgs {
		if m.Role == model.RoleSystem {
			w.write("\x00")
			w.write(m.Text)
		}
	}
	w.write("\x00\x01")
	for _, m := range msgs {
		if m.Role == model.RoleUser {
			w.write(m.Text)
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// hashBufs are the copy buffers chunkWriter feeds the digest through: the
// SHA-256 digest takes only []byte, and converting a prompt of tens of KB
// would allocate a copy of it on every call.
var hashBufs = sync.Pool{New: func() any { b := make([]byte, 4096); return &b }}

// chunkWriter writes strings to a hash through a reused buffer.
type chunkWriter struct {
	h   hash.Hash
	buf []byte
}

func (w chunkWriter) write(s string) {
	for len(s) > 0 {
		n := copy(w.buf, s)
		w.h.Write(w.buf[:n]) // a hash.Hash never returns an error
		s = s[n:]
	}
}

// turns counts the conversation's turns: messages, with a run of consecutive
// tool results (the answers to one assistant message's parallel tool calls)
// counted as one.
func turns(msgs []model.Message) int {
	n, prevTool := 0, false
	for _, m := range msgs {
		isTool := m.Role == model.RoleTool
		if !isTool || !prevTool {
			n++
		}
		prevTool = isTool
	}
	return n
}

// maxGrowth is how many turns one agent step adds: the assistant's tool
// call and the tool's result.
const maxGrowth = 2

// continues is the continuation rule: a call whose prefix matches the
// previous call of a session belongs to it when the conversation grew by at
// most one agent step (0 is a retry) and its last assistant turn made a tool
// call. prev is the previous call's turns.
func continues(prev int, msgs []model.Message) bool {
	if d := turns(msgs) - prev; d < 0 || d > maxGrowth {
		return false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == model.RoleAssistant {
			return len(msgs[i].ToolCalls) > 0
		}
	}
	return false
}
