// Package runtimediagnostic drains adapter diagnostics into bounded safe classes.
package runtimediagnostic

import (
	"errors"
	"sync"
)

type Drain struct {
	mu          sync.Mutex
	line        [512]byte
	size, total int
	overlong    bool
	class       string
	stage       string
}

// Drain all stderr without retaining it. Only the first32KiB is classified,
// using complete exact whitelisted lines; arbitrary diagnostics never escape.
func (d *Drain) Write(p []byte) (int, error) {
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
func (d *Drain) lineComplete() {
	if !d.overlong {
		line := string(d.line[:d.size])
		if d.size > 0 && d.line[d.size-1] == '\r' {
			line = line[:len(line)-1]
		}
		classes := []string{"authentication_required", "workspace_trust_required", "bypass_forbidden", "session_mismatch", "session_identity_invalid", "cli_exited", "readiness_timeout", "adapter_failed", "native_transcript_record_too_large", "native_transcript_read_limit", "native_transcript_malformed", "native_transcript_binding_invalid"}
		staged := false
		for _, stage := range []string{"unknown", "open_terminal", "native_readiness_status", "binding_persistence", "receipt_baseline", "protocol_ready_emission"} {
			for _, class := range classes {
				if line == "bria-native-startup:"+stage+":"+class {
					d.stage, d.class, staged = stage, class, true
					break
				}
			}
			if staged {
				break
			}
		}
		if !staged && d.class == "" {
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
			for _, class := range classes {
				if line == "bria-native-startup:"+class {
					d.class = class
					break
				}
			}
		}
	}
	clear(d.line[:])
	d.size = 0
	d.overlong = false
}
func (d *Drain) Finish() { d.mu.Lock(); defer d.mu.Unlock(); d.lineComplete() }
func (d *Drain) Wrap(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.class == "" {
		return err
	}
	return &startupFailure{cause: err, class: d.class, stage: d.stage}
}

type startupFailure struct {
	cause error
	class string
	stage string
}

func (e *startupFailure) Error() string { return e.cause.Error() + " (startup_class=" + e.class + ")" }
func (e *startupFailure) Unwrap() error { return e.cause }

func FailureClass(err error) string {
	var failure *startupFailure
	if errors.As(err, &failure) {
		return failure.class
	}
	return ""
}

func FailureStage(err error) string {
	var failure *startupFailure
	if errors.As(err, &failure) {
		return failure.stage
	}
	return ""
}
