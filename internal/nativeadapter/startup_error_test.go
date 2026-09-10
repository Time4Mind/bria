package nativeadapter

import (
	"errors"
	"testing"
)

func TestStartupDiagnosticCompatibilityWrappers(t *testing.T) {
	err := atStartupStage(StartupStageReceiptBaseline, errors.New("private /path"))
	if got := StartupFailureStage(err); got != StartupStageReceiptBaseline {
		t.Fatalf("startup stage=%q want %q", got, StartupStageReceiptBaseline)
	}
	if got := StartupFailureClass(err); got != "adapter_failed" {
		t.Fatalf("startup class=%q want adapter_failed", got)
	}
	if got := StartupFailureMarker(err); got != "bria-native-startup:receipt_baseline:adapter_failed" {
		t.Fatalf("startup marker=%q", got)
	}
}
