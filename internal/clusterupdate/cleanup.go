package clusterupdate

import (
	"errors"
	"path/filepath"
	"time"
)

const (
	staleUpdateArtifactAge = 24 * time.Hour
	restoreAppliedAge      = 7 * 24 * time.Hour
	previousReleaseCount   = 2
)

// ErrRecoveryBackupsOwnershipUnproven deliberately prevents cleanup of the
// legacy recovery-backups directory. There is no Bria producer or on-disk
// ownership/validation marker for that directory, so deleting its contents
// would risk deleting operator-created recovery material.
var ErrRecoveryBackupsOwnershipUnproven = errors.New(
	"recovery-backups ownership and validation marker are not proven",
)

// ErrCleanupBusy means an update is currently changing release artifacts.
var ErrCleanupBusy = errors.New("cluster update artifact cleanup is busy")

// CleanupReport describes filesystem entries removed by a cleanup primitive.
// Paths are absolute and sorted before the function returns.
type CleanupReport struct {
	Removed []string
	Skipped []string
}

// CleanupUpdateArtifacts removes only Bria-owned update artifacts below the
// configured install root. It keeps the active release, the pending rollback
// target, and the two newest other release directories containing a Bria binary.
// Staged releases and downloads older than 24 hours are removed separately.
// The install root and activation path must be absolute. A missing root is a
// successful no-op, which makes repeated cleanup idempotent.
func CleanupUpdateArtifacts(installRoot, activationPath string, now time.Time) (CleanupReport, error) {
	if !filepath.IsAbs(installRoot) || !filepath.IsAbs(activationPath) {
		return CleanupReport{}, errors.New("cleanup paths must be absolute")
	}
	return cleanupUpdateArtifacts(filepath.Clean(installRoot), filepath.Clean(activationPath), cleanupNow(now))
}

// CleanupRestoreAppliedArtifacts removes only files produced by
// applyPendingClusterRestore: restore.applied.<16 lowercase hex>.json. These
// files are direct children of the configured Bria DataDir and are retained
// for seven days. Symlinks, directories, malformed names, and external paths
// are never followed or removed.
func CleanupRestoreAppliedArtifacts(dataRoot string, now time.Time) (CleanupReport, error) {
	if !filepath.IsAbs(dataRoot) {
		return CleanupReport{}, errors.New("cleanup paths must be absolute")
	}
	return cleanupRestoreAppliedArtifacts(filepath.Clean(dataRoot), cleanupNow(now))
}

// CleanupRecoveryBackups is intentionally a safe blocker. The current
// recovery-backups directory has operator-created contents and no Bria-owned
// manifest or validation marker. A future implementation must accept an
// explicit Bria manifest (including verified bundle identity) before applying
// the 30-day retention policy.
func CleanupRecoveryBackups(_ string, _ time.Time) (CleanupReport, error) {
	return CleanupReport{}, ErrRecoveryBackupsOwnershipUnproven
}

// CleanupArtifacts is the Manager-owned entry point. The busy check prevents
// a scheduler from deleting an extracted/preflight candidate while install is
// running. Callers that use the lower-level primitive directly own this
// synchronization contract.
func (m *Manager) CleanupArtifacts(now time.Time) (CleanupReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if activeManagerPhase(m.status.Phase) {
		return CleanupReport{}, ErrCleanupBusy
	}
	updateReport, err := CleanupUpdateArtifacts(m.config.InstallRoot, m.config.ActivationPath, now)
	if err != nil {
		return updateReport, err
	}
	return updateReport, nil
}
