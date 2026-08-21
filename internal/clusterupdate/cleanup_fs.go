package clusterupdate

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type releaseCandidate struct {
	path       string
	modTime    time.Time
	successful bool
}

func cleanupUpdateArtifacts(installRoot, activationPath string, now time.Time) (CleanupReport, error) {
	root, present, err := secureCleanupRoot(installRoot)
	if err != nil || !present {
		return CleanupReport{}, err
	}
	releasesRoot, releasesPresent, err := secureCleanupDirectory(filepath.Join(root, "releases"))
	if err != nil {
		return CleanupReport{}, err
	}
	if !releasesPresent {
		releasesRoot = filepath.Join(root, "releases")
	}
	lexicalReleasesRoot := filepath.Join(installRoot, "releases")
	protected := make(map[string]struct{})
	if releasesPresent {
		if err := protectPathReference(activationPath, releasesRoot, lexicalReleasesRoot, protected); err != nil {
			return CleanupReport{}, err
		}
		if err := protectPendingReference(filepath.Join(root, "update-pending.json"), releasesRoot, lexicalReleasesRoot, protected); err != nil {
			return CleanupReport{}, err
		}
	}
	var report CleanupReport
	if err := cleanupStaleEntries(root, now, staleUpdateArtifactAge,
		func(name string) bool { return strings.HasPrefix(name, ".download-") },
		protected, &report); err != nil {
		return report, err
	}
	if !releasesPresent {
		finishCleanupReport(&report)
		return report, nil
	}
	if err := cleanupStaleEntries(releasesRoot, now, staleUpdateArtifactAge,
		func(name string) bool { return strings.HasSuffix(name, ".stage") },
		protected, &report); err != nil {
		return report, err
	}
	candidates, err := releaseCandidates(releasesRoot, &report)
	if err != nil {
		return report, err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	keep := make(map[string]struct{}, len(protected)+previousReleaseCount)
	for path := range protected {
		keep[path] = struct{}{}
	}
	keptSuccessful := 0
	for _, candidate := range candidates {
		if _, alreadyProtected := protected[candidate.path]; alreadyProtected {
			continue
		}
		if candidate.successful && keptSuccessful < previousReleaseCount {
			keep[candidate.path] = struct{}{}
			keptSuccessful++
		}
	}
	for _, candidate := range candidates {
		if _, ok := keep[candidate.path]; ok {
			continue
		}
		if err := removeOwnedEntry(candidate.path, releasesRoot); err != nil {
			return report, err
		}
		report.Removed = append(report.Removed, candidate.path)
	}
	finishCleanupReport(&report)
	return report, nil
}

func cleanupRestoreAppliedArtifacts(dataRoot string, now time.Time) (CleanupReport, error) {
	root, present, err := secureCleanupRoot(dataRoot)
	if err != nil || !present {
		return CleanupReport{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return CleanupReport{}, fmt.Errorf("read restore artifact root: %w", err)
	}
	var report CleanupReport
	for _, entry := range entries {
		if !isRestoreAppliedName(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return report, fmt.Errorf("inspect restore artifact %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			report.Skipped = append(report.Skipped, path)
			continue
		}
		if !olderThan(info.ModTime(), now, restoreAppliedAge) {
			continue
		}
		if err := removeOwnedEntry(path, root); err != nil {
			return report, err
		}
		report.Removed = append(report.Removed, path)
	}
	finishCleanupReport(&report)
	return report, nil
}

func releaseCandidates(releasesRoot string, report *CleanupReport) ([]releaseCandidate, error) {
	entries, err := os.ReadDir(releasesRoot)
	if err != nil {
		return nil, fmt.Errorf("read release root: %w", err)
	}
	candidates := make([]releaseCandidate, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(releasesRoot, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("inspect release %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || strings.HasSuffix(entry.Name(), ".stage") {
			if info.Mode()&os.ModeSymlink != 0 {
				report.Skipped = append(report.Skipped, path)
			}
			continue
		}
		candidates = append(candidates, releaseCandidate{
			path: path, modTime: info.ModTime(), successful: hasReleaseBinary(path),
		})
	}
	return candidates, nil
}

func cleanupStaleEntries(root string, now time.Time, maxAge time.Duration,
	match func(string) bool, protected map[string]struct{}, report *CleanupReport) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read cleanup root: %w", err)
	}
	for _, entry := range entries {
		if !match(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if _, ok := protected[path]; ok {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect stale artifact %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			report.Skipped = append(report.Skipped, path)
			continue
		}
		if !olderThan(info.ModTime(), now, maxAge) {
			continue
		}
		if err := removeOwnedEntry(path, root); err != nil {
			return err
		}
		report.Removed = append(report.Removed, path)
	}
	return nil
}

func protectPendingReference(pendingPath, releasesRoot, lexicalReleasesRoot string, protected map[string]struct{}) error {
	info, err := os.Lstat(pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect pending update: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("pending update record is not a regular file")
	}
	data, err := os.ReadFile(pendingPath)
	if err != nil {
		return fmt.Errorf("read pending update: %w", err)
	}
	var pending pendingUpdate
	if err := json.Unmarshal(data, &pending); err != nil || pending.UpdateID == "" {
		return errors.New("pending update record is invalid")
	}
	if pending.Previous == "" {
		return nil
	}
	if !filepath.IsAbs(pending.Previous) {
		return errors.New("pending update previous path is not absolute")
	}
	return protectPathReference(pending.Previous, releasesRoot, lexicalReleasesRoot, protected)
}

func protectPathReference(path, releasesRoot, lexicalReleasesRoot string, protected map[string]struct{}) error {
	if !filepath.IsAbs(path) {
		return errors.New("referenced cleanup path is not absolute")
	}
	if releasePath, ok := releaseEntryPath(filepath.Clean(lexicalReleasesRoot), filepath.Clean(path)); ok {
		protected[filepath.Join(releasesRoot, filepath.Base(releasePath))] = struct{}{}
	}
	clean := canonicalCleanupPath(path)
	if !pathWithin(releasesRoot, clean) {
		return nil
	}
	if releasePath, ok := releaseEntryPath(releasesRoot, clean); ok {
		protected[releasePath] = struct{}{}
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve referenced cleanup path %q: %w", path, err)
	}
	if releasePath, ok := releaseEntryPath(releasesRoot, resolved); ok {
		protected[releasePath] = struct{}{}
	}
	return nil
}

func releaseEntryPath(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", false
	}
	first := strings.Split(relative, string(os.PathSeparator))[0]
	if first == "" || first == "." {
		return "", false
	}
	return filepath.Join(root, first), true
}

func canonicalCleanupPath(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	parent := filepath.Dir(clean)
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(filepath.Clean(resolved), filepath.Base(clean))
	}
	return clean
}

func hasReleaseBinary(path string) bool {
	for _, name := range []string{"bria", "bria.exe"} {
		info, err := os.Lstat(filepath.Join(path, name))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func isRestoreAppliedName(name string) bool {
	const prefix, suffix = "restore.applied.", ".json"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(digest) != 16 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil && strings.ToLower(digest) == digest
}
