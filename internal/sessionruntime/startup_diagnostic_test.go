package sessionruntime

import (
	"errors"
	"strings"
	"testing"
)

func TestStartupDiagnosticBoundedWhitelist(t *testing.T) {
	for _, input := range []string{
		"token=secret bria-native-startup:bypass_forbidden\n",
		strings.Repeat("x", 513) + "\n",
		strings.Repeat("x", 32<<10) + "\nbria-native-startup:bypass_forbidden\n",
		"bria-native-startup:secret\n",
	} {
		d := &startupDiagnostic{}
		_, _ = d.Write([]byte(input))
		d.finish()
		err := d.wrap(errors.New("startup failed"))
		if StartupFailureClass(err) != "" || err.Error() != "startup failed" {
			t.Fatal("untrusted stderr escaped whitelist")
		}
	}
}

func TestStartupDiagnosticRetainsOnlyAllowlistedStageAndClass(t *testing.T) {
	d := &startupDiagnostic{}
	_, _ = d.Write([]byte("native CLI authentication required\nbria-native-startup:native_readiness_status:authentication_required\n"))
	d.finish()
	err := d.wrap(errors.New("startup failed"))
	if got := StartupFailureClass(err); got != "authentication_required" {
		t.Fatalf("failure class = %q, want authentication_required", got)
	}
	if got := StartupFailureStage(err); got != "native_readiness_status" {
		t.Fatalf("failure stage = %q, want native_readiness_status", got)
	}
}

func TestStartupDiagnosticRejectsUnknownStage(t *testing.T) {
	d := &startupDiagnostic{}
	_, _ = d.Write([]byte("bria-native-startup:private_stage:adapter_failed\n"))
	d.finish()
	err := d.wrap(errors.New("startup failed"))
	if StartupFailureClass(err) != "" || StartupFailureStage(err) != "" {
		t.Fatalf("unknown stage escaped whitelist: class=%q stage=%q", StartupFailureClass(err), StartupFailureStage(err))
	}
}
