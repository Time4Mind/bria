// Package nativecapture bounds native terminal payloads and fingerprints their
// exact bytes independently of rendering and transport composition.
package nativecapture

import (
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"
)

const DefaultLimitKiB = 48

// Bound retains the UTF-8-aligned terminal tail at a supported capture budget.
func Bound(text string, limitKiB int) string {
	if limitKiB != 48 && limitKiB != 64 && limitKiB != 86 {
		limitKiB = DefaultLimitKiB
	}
	maxBytes := limitKiB * 1024
	if len(text) > maxBytes {
		text = text[len(text)-maxBytes:]
		for !utf8.ValidString(text) {
			text = text[1:]
		}
	}
	return text
}

// Hash identifies the exact captured bytes, including whitespace.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
