package orphanresume_test

import (
	"path/filepath"
	"testing"

	"bria/internal/orphanresume"
)

func TestCleanupAbsentIdentityOrWorkdirIsNoop(t *testing.T) {
	workdir := t.TempDir()
	for _, tc := range []struct{ sessionID, workdir string }{
		{"", workdir}, {" \t ", workdir},
		{"orphanresume-test", filepath.Join(workdir, "absent")},
	} {
		if err := orphanresume.Cleanup(tc.sessionID, tc.workdir); err != nil {
			t.Fatal(err)
		}
	}
}
