package nativeadapter

import (
	"context"
	"errors"
	"strings"
)

// StartupFailureClass is the only error detail allowed onto adapter stderr.
// Unknown errors never expose paths, provider output, credentials or prompts.
func StartupFailureClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "readiness_timeout"
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		switch line {
		case "native CLI authentication required":
			return "authentication_required"
		case "native CLI workspace trust confirmation required":
			return "workspace_trust_required"
		case "native CLI bypass forbidden for current operating user":
			return "bypass_forbidden"
		case "native Codex resumed a different session":
			return "session_mismatch"
		case "native Claude requires declared session UUID":
			return "session_identity_invalid"
		case "native CLI exited":
			return "cli_exited"
		}
	}
	return "adapter_failed"
}
