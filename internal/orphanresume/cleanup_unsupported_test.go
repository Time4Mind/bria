//go:build !linux

package orphanresume_test

import (
	"testing"

	"bria/internal/orphanresume"
)

func TestCleanupUnsupportedPlatformIsNoop(t *testing.T) {
	if err := orphanresume.Cleanup("valid-resume-session", t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
