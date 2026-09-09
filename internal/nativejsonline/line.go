// Package nativejsonline bounds memory while framing native JSONL records.
// Oversized records expose a compact diagnostic-only candidate, never an event.
package nativejsonline

import (
	"bytes"
	"errors"
	"unicode/utf8"
)

var ErrLimit = errors.New("native JSON projection limit exceeded")
var ErrMalformed = errors.New("native JSON string malformed")

// Line retains two Max-byte buffers and a bounded index of discarded strings.
// Full JSON syntax and duplicate keys must be validated by the caller. Long
// string values are replaced with empty strings only in the diagnostic candidate.
// Keys are never shortened; excessive key/structure size fails with ErrLimit.
type Line struct {
	Max                      int
	raw, compact             []byte
	Discarded                []int // Opening-quote offsets in the compact candidate.
	Size                     int64
	Large                    bool
	quoted, escaped, dropped bool
	hex                      int
	start                    int
	runeBytes                [4]byte
	runeSize                 int
	invalid                  bool
	containers               [128]byte
	depth                    int
	previous                 byte
	key                      bool
}

// Clone creates an independent bounded checkpoint for an atomic reader batch.
// The original can be retried unchanged after cancellation or a parse error.
func (l *Line) Clone() *Line {
	if l == nil {
		return nil
	}
	cloned := *l
	cloned.raw, cloned.compact = bytes.Clone(l.raw), bytes.Clone(l.compact)
	cloned.Discarded = append([]int(nil), l.Discarded...)
	return &cloned
}

// Push returns true only at a complete newline-terminated record boundary.
func (l *Line) Push(b byte) (done bool, err error) {
	// Preserve the historical limit error for oversized malformed records.
	// A short unfinished record is not committed until its newline arrives.
	defer func() {
		if errors.Is(err, ErrMalformed) {
			l.invalid = true
			if !l.Large && b != '\n' {
				err = nil
			}
		}
	}()
	if b == '\n' {
		if l.invalid || l.quoted || l.runeSize != 0 {
			return false, ErrMalformed
		}
		return true, nil
	}
	l.Size++
	if !l.Large {
		if len(l.raw) < l.Max {
			l.raw = l.appendByte(l.raw, b)
		} else {
			l.Large = true
			l.raw = nil
		}
	}
	if l.invalid {
		return false, ErrMalformed
	}
	if err := l.validateUTF8(b); err != nil {
		return false, err
	}
	keep := true
	if l.quoted {
		if b < 0x20 {
			return false, ErrMalformed
		}
		closing := false
		switch {
		case l.hex > 0:
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F') {
				return false, ErrMalformed
			}
			l.hex--
		case l.escaped:
			l.escaped = false
			switch b {
			case 'u':
				l.hex = 4
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			default:
				return false, ErrMalformed
			}
		case b == '\\':
			l.escaped = true
		case b == '"':
			closing = true
			l.quoted = false
		}
		if l.dropped {
			keep = closing
		} else if !l.key && !closing && len(l.compact)-l.start >= min(8192, l.Max/2, l.Max-l.start-1) {
			l.compact = l.compact[:l.start+1]
			l.Discarded = append(l.Discarded, l.start)
			l.dropped = true
			keep = false
		}
	} else if b == '"' {
		l.key = l.depth > 0 && l.containers[l.depth-1] == '{' && (l.previous == '{' || l.previous == ',')
		l.quoted = true
		l.dropped = false
		l.start = len(l.compact)
	} else {
		switch b {
		case '{', '[':
			if l.depth == len(l.containers) {
				return false, ErrMalformed
			}
			l.containers[l.depth] = b
			l.depth++
		case '}', ']':
			if l.depth == 0 || b == '}' && l.containers[l.depth-1] != '{' || b == ']' && l.containers[l.depth-1] != '[' {
				return false, ErrMalformed
			}
			l.depth--
		}
		if b != ' ' && b != '\t' && b != '\r' {
			l.previous = b
		}
	}
	if keep {
		if len(l.compact) >= l.Max {
			return false, ErrLimit
		}
		l.compact = l.appendByte(l.compact, b)
	}
	return false, nil
}

func (l *Line) appendByte(dst []byte, b byte) []byte {
	if len(dst) == cap(dst) {
		next := make([]byte, len(dst), min(l.Max, max(64, 2*cap(dst))))
		copy(next, dst)
		dst = next
	}
	return append(dst, b)
}

func (l *Line) Bytes() []byte {
	if l.Large {
		return l.compact
	}
	return l.raw
}

func (l *Line) validateUTF8(b byte) error {
	if l.runeSize == 0 && b < utf8.RuneSelf {
		return nil
	}
	if l.runeSize == len(l.runeBytes) {
		return ErrMalformed
	}
	l.runeBytes[l.runeSize] = b
	l.runeSize++
	value := l.runeBytes[:l.runeSize]
	if utf8.FullRune(value) {
		if !utf8.Valid(value) {
			return ErrMalformed
		}
		l.runeSize = 0
	}
	return nil
}
