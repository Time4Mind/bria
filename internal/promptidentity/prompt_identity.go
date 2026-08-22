// Package promptidentity derives content-free identities for provider prompts.
package promptidentity

import (
	"crypto/sha256"
	"encoding/hex"
)

const DigestLength = sha256.Size * 2

// Digest returns the SHA-256 identity of the exact UTF-8 prompt bytes. It is
// used only for in-memory comparison; neither the digest nor prompt is logged.
func Digest(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}
