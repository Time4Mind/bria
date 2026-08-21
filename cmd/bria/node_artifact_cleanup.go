package main

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/processlog"
)

const artifactCleanupInterval = 24 * time.Hour

type updateArtifactCleaner interface {
	CleanupArtifacts(time.Time) (clusterupdate.CleanupReport, error)
}

func cleanupNodeArtifacts(
	dataDir string,
	updates updateArtifactCleaner,
	now time.Time,
) (int, error) {
	removed := 0
	var joined error
	if updates != nil {
		report, err := updates.CleanupArtifacts(now)
		removed += len(report.Removed)
		if err != nil && !errors.Is(err, clusterupdate.ErrCleanupBusy) {
			joined = errors.Join(joined, err)
		}
	}
	report, err := clusterupdate.CleanupRestoreAppliedArtifacts(dataDir, now)
	removed += len(report.Removed)
	if err != nil {
		joined = errors.Join(joined, err)
	}
	return removed, joined
}

func runNodeArtifactCleanup(
	ctx context.Context,
	dataDir string,
	updates updateArtifactCleaner,
) {
	ticker := time.NewTicker(artifactCleanupInterval)
	defer ticker.Stop()
	cleanup := func() {
		removed, err := cleanupNodeArtifacts(dataDir, updates, time.Now().UTC())
		if err != nil && ctx.Err() == nil {
			processlog.Criticalf("bria artifact cleanup: %v", err)
			return
		}
		if removed > 0 {
			processlog.Servicef("bria artifact cleanup: removed=%d", removed)
		}
	}
	cleanup()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
