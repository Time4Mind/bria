package clusterupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/binaryidentity"
)

func Watchdog(ctx context.Context, installRoot, updateID string, pid int, timeout time.Duration) error {
	if !filepath.IsAbs(installRoot) || strings.TrimSpace(updateID) == "" || pid <= 1 || timeout <= 0 {
		return errors.New("invalid update watchdog request")
	}
	pendingPath := filepath.Join(filepath.Clean(installRoot), "update-pending.json")
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(pendingPath); errors.Is(err, os.ErrNotExist) {
				return nil
			}
		case <-timer.C:
			return rollbackPending(pendingPath, updateID, pid)
		}
	}
}

func rollbackPending(path, updateID string, pid int) error {
	pending, err := restorePending(path, updateID)
	if err != nil {
		return err
	}
	status := Status{
		NodeID: pending.NodeID, UpdateID: pending.UpdateID, Version: pending.Version,
		Phase: PhaseFailed, Error: "new version did not become ready; rolled back",
	}
	data, _ := json.Marshal(status)
	_ = os.WriteFile(filepath.Join(filepath.Dir(path), "update-status.json"), data, 0o600)
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
	return nil
}

func restorePending(path, updateID string) (pendingUpdate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pendingUpdate{}, err
	}
	var pending pendingUpdate
	if json.Unmarshal(data, &pending) != nil || pending.UpdateID != updateID ||
		!filepath.IsAbs(pending.Previous) || !filepath.IsAbs(pending.CurrentLink) {
		return pendingUpdate{}, errors.New("pending update rollback record is invalid")
	}
	if pending.NextSHA256 != "" {
		releasesRoot := filepath.Join(filepath.Dir(path), "releases")
		if !pendingReleaseTarget(releasesRoot, pending.Previous) ||
			!pendingReleaseTarget(releasesRoot, pending.Next) {
			return pendingUpdate{}, errors.New("pending update release target is invalid")
		}
	}
	previousBinary := pending.Previous
	if info, err := os.Stat(pending.Previous); err == nil && info.IsDir() {
		previousBinary = releaseBinary(pending.Previous)
	}
	previousSHA256, err := binaryidentity.SHA256(previousBinary)
	if err != nil || pending.PreviousSHA256 != "" && pending.PreviousSHA256 != previousSHA256 {
		return pendingUpdate{}, errors.New("pending update rollback binary is invalid")
	}
	if err := replaceSymlink(pending.CurrentLink, pending.Previous); err != nil {
		return pendingUpdate{}, err
	}
	_ = os.Remove(path)
	return pending, nil
}

func pendingReleaseTarget(releasesRoot, target string) bool {
	if !filepath.IsAbs(releasesRoot) || !filepath.IsAbs(target) {
		return false
	}
	release, ok := releaseEntryPath(filepath.Clean(releasesRoot), filepath.Clean(target))
	return ok && pathWithin(filepath.Clean(release), filepath.Clean(target))
}

func ConfirmInstalled(installRoot, version string, binarySHA256 ...string) error {
	path := filepath.Join(installRoot, "update-pending.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending pendingUpdate
	if json.Unmarshal(data, &pending) != nil {
		return errors.New("pending update record is invalid")
	}
	if pending.NextSHA256 == "" {
		return errors.New("legacy pending update cannot be confirmed without binary identity")
	}
	if len(binarySHA256) != 1 || len(binarySHA256[0]) != 64 {
		return errors.New("running binary identity is required to confirm update")
	}
	releasesRoot := filepath.Join(installRoot, "releases")
	if !pendingReleaseTarget(releasesRoot, pending.Previous) ||
		!pendingReleaseTarget(releasesRoot, pending.Next) {
		return errors.New("pending update release target is invalid")
	}
	runningSHA256 := binarySHA256[0]
	current, err := filepath.EvalSymlinks(pending.CurrentLink)
	if err != nil {
		return errors.New("resolve pending update activation")
	}
	if pending.PreviousSHA256 != "" && runningSHA256 == pending.PreviousSHA256 &&
		sameActivationTarget(current, pending.Previous) {
		return os.Remove(path)
	}
	if pending.Version != version || runningSHA256 != pending.NextSHA256 ||
		!sameActivationTarget(current, pending.Next) {
		return errors.New("pending update does not match running version")
	}
	return os.Remove(path)
}

func sameActivationTarget(resolved, target string) bool {
	resolvedTarget, err := filepath.EvalSymlinks(target)
	return err == nil && filepath.Clean(resolved) == filepath.Clean(resolvedTarget)
}
