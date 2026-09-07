package promptpreprocesscommand

import "strings"

// A model receipt can precede a timeout. Keep the error chain/fallback intact:
// execution-model confirmation is not successful preprocessing completion.
type executionError struct {
	cause         error
	modelEvidence string
}

func (e *executionError) Error() string { return e.cause.Error() }
func (e *executionError) Unwrap() error { return e.cause }

// Codex's non-JSON exec startup header reports its execution model before the
// echoed user input. Do not inspect model-looking text in user/model output,
// persist raw stderr, or infer a price from this receipt.
func confirmedCodexModel(stderr, requested string) bool {
	lines := strings.Split(stderr, "\n")
	banner, header, found := false, false, false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !banner {
			if line == "" || line == "Reading prompt from stdin..." {
				continue
			}
			if strings.HasPrefix(line, "OpenAI Codex v") {
				banner = true
			} else {
				return false
			}
			continue
		}
		if line == "--------" {
			if header {
				return found
			}
			header = true
			continue
		}
		if !header {
			return false
		}
		if strings.HasPrefix(line, "model:") {
			if found || strings.TrimSpace(strings.TrimPrefix(line, "model:")) != requested {
				return false
			}
			found = true
		}
	}
	return false
}
