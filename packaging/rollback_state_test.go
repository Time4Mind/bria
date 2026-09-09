package packaging_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRollbackRejectsTargetWithoutStateCompatibilityReceipt(t *testing.T) {
	root := t.TempDir()
	trust, private := signingFixture(t, root)
	install := filepath.Join(root, "install")
	if err := os.Mkdir(install, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old := signedReleaseFixture(t, root, "1.0.0", "1.0.0", trust, private, false, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, old, trust, install, config)
	current := signedReleaseFixture(t, root, "1.1.0", "1.1.0", trust, private, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, current, trust, install, config)
	if output, err := scriptCommand("rollback-install.sh", install, config, trust).CombinedOutput(); err == nil {
		t.Fatalf("incompatible target accepted: %s", output)
	}
	assertLink(t, filepath.Join(install, "current"), "releases/1.1.0")
	assertLink(t, filepath.Join(install, "previous"), "releases/1.0.0")
	assertFileContent(t, config, "{}\n")
	if _, err := os.Stat(filepath.Join(install, ".rollback-transaction")); !os.IsNotExist(err) {
		t.Fatalf("rejected rollback began a transaction: %v", err)
	}
	marker := "releases/1.1.0\nreleases/1.0.0\n"
	if err := os.WriteFile(filepath.Join(install, ".rollback-transaction"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := scriptCommand("rollback-install.sh", install, config, trust).CombinedOutput(); err == nil {
		t.Fatalf("interrupted rollback bypassed compatibility: %s", output)
	}
	assertLink(t, filepath.Join(install, "current"), "releases/1.1.0")
	assertLink(t, filepath.Join(install, "previous"), "releases/1.0.0")
	assertFileContent(t, filepath.Join(install, ".rollback-transaction"), marker)
}
