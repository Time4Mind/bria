package sessionruntime

import (
	"errors"
	"sync"
)

type startupDiagnostic struct {
	mu          sync.Mutex
	line        [512]byte
	size, total int
	overlong    bool
	class       string
}

// Drain all stderr without retaining it. Only the first32KiB is classified,
// using complete exact whitelisted lines; arbitrary diagnostics never escape.
func (d *startupDiagnostic) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, b := range p {
		if d.total >= 32<<10 {
			break
		}
		d.total++
		if b == '\n' {
			d.lineComplete()
			continue
		}
		if d.size == len(d.line) {
			d.overlong = true
			continue
		}
		d.line[d.size] = b
		d.size++
	}
	return len(p), nil
}
func (d *startupDiagnostic) lineComplete() {
	if !d.overlong && d.class == "" {
		line := string(d.line[:d.size])
		if d.size > 0 && d.line[d.size-1] == '\r' {
			line = line[:len(line)-1]
		}
		known := map[string]string{
			"native CLI authentication required":                     "authentication_required",
			"native CLI workspace trust confirmation required":       "workspace_trust_required",
			"native CLI bypass forbidden for current operating user": "bypass_forbidden",
			"native Codex resumed a different session":               "session_mismatch",
			"native Claude requires declared session UUID":           "session_identity_invalid",
			"native CLI exited":                                      "cli_exited",
			"native Claude adapter failed":                           "adapter_failed",
		}
		if class := known[line]; class != "" {
			d.class = class
		}
		for _, class := range []string{"authentication_required", "workspace_trust_required", "bypass_forbidden", "session_mismatch", "session_identity_invalid", "cli_exited", "readiness_timeout", "adapter_failed"} {
			if line == "bria-native-startup:"+class {
				d.class = class
				break
			}
		}
	}
	clear(d.line[:])
	d.size = 0
	d.overlong = false
}
func (d *startupDiagnostic) finish() { d.mu.Lock(); defer d.mu.Unlock(); d.lineComplete() }
func (d *startupDiagnostic) wrap(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.class == "" {
		return err
	}
	return &startupFailure{cause: err, class: d.class}
}

type startupFailure struct {
	cause error
	class string
}

func (e *startupFailure) Error() string { return e.cause.Error() + " (startup_class=" + e.class + ")" }
func (e *startupFailure) Unwrap() error { return e.cause }

func StartupFailureClass(err error) string {
	var failure *startupFailure
	if errors.As(err, &failure) {
		return failure.class
	}
	return ""
}
