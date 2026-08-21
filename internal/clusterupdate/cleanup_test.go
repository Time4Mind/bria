package clusterupdate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestCleanupUpdateArtifactsKeepsReferencesAndNewestSuccessful(t *testing.T) {
	root := t.TempDir()
	releases := filepath.Join(root, "releases")
	if err := os.Mkdir(releases, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	active := filepath.Join(releases, "active")
	pending := filepath.Join(releases, "pending")
	newest := filepath.Join(releases, "newest")
	second := filepath.Join(releases, "second")
	old := filepath.Join(releases, "old")
	failed := filepath.Join(releases, "failed")
	for _, release := range []string{active, pending, newest, second, old} {
		writeRelease(t, release)
	}
	if err := os.Mkdir(failed, 0o700); err != nil {
		t.Fatal(err)
	}
	setMTime(t, active, now.Add(-30*24*time.Hour))
	setMTime(t, pending, now.Add(-20*24*time.Hour))
	setMTime(t, newest, now.Add(-time.Hour))
	setMTime(t, second, now.Add(-2*time.Hour))
	setMTime(t, old, now.Add(-3*24*time.Hour))
	setMTime(t, failed, now.Add(-3*24*time.Hour))
	activation := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join(active, "bria"), activation); err != nil {
		t.Fatal(err)
	}
	if err := writePending(filepath.Join(root, "update-pending.json"), pendingUpdate{
		UpdateID: "job", Version: "v2", Previous: filepath.Join(pending, "bria"),
	}); err != nil {
		t.Fatal(err)
	}
	oldDownload := filepath.Join(root, ".download-old.tar.gz")
	freshDownload := filepath.Join(root, ".download-fresh.tar.gz")
	writeFile(t, oldDownload)
	writeFile(t, freshDownload)
	setMTime(t, oldDownload, now.Add(-25*time.Hour))
	setMTime(t, freshDownload, now.Add(-time.Hour))
	oldStage := filepath.Join(releases, "candidate.stage")
	freshStage := filepath.Join(releases, "fresh.stage")
	if err := os.MkdirAll(oldStage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(freshStage, 0o700); err != nil {
		t.Fatal(err)
	}
	setMTime(t, oldStage, now.Add(-25*time.Hour))
	setMTime(t, freshStage, now.Add(-time.Hour))

	report, err := CleanupUpdateArtifacts(root, activation, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{active, pending, newest, second, freshDownload, freshStage} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("kept path %q: %v", path, err)
		}
	}
	for _, path := range []string{old, failed, oldDownload, oldStage} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed path %q: %v", path, err)
		}
	}
	wantRemoved := []string{
		canonicalTestPath(t, old), canonicalTestPath(t, failed),
		canonicalTestPath(t, oldDownload), canonicalTestPath(t, oldStage),
	}
	sort.Strings(wantRemoved)
	if !reflect.DeepEqual(report.Removed, wantRemoved) {
		t.Fatalf("removed=%v, want %v", report.Removed, wantRemoved)
	}

	report, err = CleanupUpdateArtifacts(root, activation, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("second cleanup removed=%v", report.Removed)
	}
}

func TestCleanupUpdateArtifactsNeverFollowsSymlinkEntries(t *testing.T) {
	root := t.TempDir()
	releases := filepath.Join(root, "releases")
	if err := os.Mkdir(releases, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	externalFile := filepath.Join(external, "keep")
	writeFile(t, externalFile)
	symlinkRelease := filepath.Join(releases, "external-release")
	if err := os.Symlink(external, symlinkRelease); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(releases, "old")
	writeRelease(t, old)
	setMTime(t, old, time.Now().Add(-3*24*time.Hour))
	activation := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join(external, "bria"), activation); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupUpdateArtifacts(root, activation, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(symlinkRelease); err != nil {
		t.Fatalf("release symlink changed: %v", err)
	}
	if _, err := os.Stat(externalFile); err != nil {
		t.Fatalf("external file changed: %v", err)
	}
}

func TestCleanupUpdateArtifactsKeepsTwoPreviousWhenActiveIsNewest(t *testing.T) {
	root := t.TempDir()
	releases := filepath.Join(root, "releases")
	if err := os.Mkdir(releases, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000_000, 0).UTC()
	active := filepath.Join(releases, "active")
	previousOne := filepath.Join(releases, "previous-one")
	previousTwo := filepath.Join(releases, "previous-two")
	old := filepath.Join(releases, "old")
	for _, release := range []string{active, previousOne, previousTwo, old} {
		writeRelease(t, release)
	}
	setMTime(t, active, now)
	setMTime(t, previousOne, now.Add(-time.Hour))
	setMTime(t, previousTwo, now.Add(-2*time.Hour))
	setMTime(t, old, now.Add(-3*time.Hour))
	activation := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join(active, "bria"), activation); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupUpdateArtifacts(root, activation, now); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{active, previousOne, previousTwo} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("release %q was not retained: %v", kept, err)
		}
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old release was retained: %v", err)
	}
}

func TestCleanupUpdateArtifactsProtectsPreviousAndRunningTargets(t *testing.T) {
	root := t.TempDir()
	releases := filepath.Join(root, "releases")
	if err := os.Mkdir(releases, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(3_000_000, 0).UTC()
	active := filepath.Join(releases, "active")
	previous := filepath.Join(releases, "previous-release")
	running := filepath.Join(releases, "running")
	newest := filepath.Join(releases, "newest")
	second := filepath.Join(releases, "second")
	old := filepath.Join(releases, "old")
	for _, release := range []string{active, previous, running, newest, second, old} {
		writeRelease(t, release)
	}
	for index, release := range []string{active, previous, running, newest, second, old} {
		setMTime(t, release, now.Add(-time.Duration(index+1)*time.Hour))
	}
	activation := filepath.Join(root, "current")
	if err := os.Symlink(active, activation); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(previous, filepath.Join(root, "previous")); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupUpdateArtifacts(root, activation, now, filepath.Join(running, "bria")); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{active, previous, running, newest, second} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("protected release %q was removed: %v", kept, err)
		}
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unprotected old release remains: %v", err)
	}
}

func TestCleanupRestoreAppliedArtifactsKeepsRecentAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	old := filepath.Join(root, "restore.applied.0123456789abcdef.json")
	recent := filepath.Join(root, "restore.applied.fedcba9876543210.json")
	writeFile(t, old)
	writeFile(t, recent)
	setMTime(t, old, now.Add(-8*24*time.Hour))
	setMTime(t, recent, now.Add(-time.Hour))
	external := filepath.Join(t.TempDir(), "external")
	writeFile(t, external)
	link := filepath.Join(root, "restore.applied.aaaaaaaaaaaaaaaa.json")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}

	report, err := CleanupRestoreAppliedArtifacts(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old applied artifact remains: %v", err)
	}
	for _, path := range []string{recent, link, external} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("kept path %q: %v", path, err)
		}
	}
	if !reflect.DeepEqual(report.Removed, []string{canonicalTestPath(t, old)}) {
		t.Fatalf("removed=%v, want [%s]", report.Removed, canonicalTestPath(t, old))
	}
}

func TestCleanupRejectsSymlinkRootAndRecoveryBackupsAreBlocked(t *testing.T) {
	root := t.TempDir()
	realRoot := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(realRoot, "releases"), 0o700); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(root, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanupUpdateArtifacts(linkRoot, filepath.Join(linkRoot, "current"), time.Now()); err == nil {
		t.Fatal("symlink install root was accepted")
	}
	recoveryRoot := filepath.Join(root, "recovery-backups")
	if err := os.Mkdir(recoveryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(recoveryRoot, "operator-created.json")
	writeFile(t, artifact)
	if _, err := CleanupRecoveryBackups(recoveryRoot, time.Now()); !errors.Is(err, ErrRecoveryBackupsOwnershipUnproven) {
		t.Fatalf("recovery cleanup error=%v", err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("recovery artifact changed: %v", err)
	}
}

func TestManagerCleanupArtifactsRefusesActiveUpdate(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "releases", "candidate.stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	setMTime(t, stage, now.Add(-25*time.Hour))
	manager := &Manager{
		config: ManagerConfig{
			InstallRoot:    root,
			ActivationPath: filepath.Join(root, "current"),
		},
		status: Status{Phase: PhaseDownloading},
	}
	if _, err := manager.CleanupArtifacts(now); !errors.Is(err, ErrCleanupBusy) {
		t.Fatalf("active cleanup error=%v", err)
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("active update artifact changed: %v", err)
	}
	manager.status.Phase = PhaseIdle
	if _, err := manager.CleanupArtifacts(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle cleanup left stage: %v", err)
	}
}

func writeRelease(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(path, "bria"))
	if err := os.Chmod(filepath.Join(path, "bria"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setMTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, filepath.Base(path))
}
