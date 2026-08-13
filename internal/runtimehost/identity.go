package runtimehost

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// TmuxWindowName is stable across daemon and host restarts without exposing a
// session identifier to tmux command parsing or length limits.
func TmuxWindowName(nodeID, sessionID string) string {
	digest := sha256.Sum256([]byte(nodeID + "\x00" + sessionID))
	return "bria-" + hex.EncodeToString(digest[:12])
}

func TmuxTarget(tmuxSession, nodeID, sessionID string) string {
	return strings.TrimSpace(tmuxSession) + ":" + TmuxWindowName(nodeID, sessionID)
}
