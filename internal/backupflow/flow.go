// Package backupflow defines the product-level backup and restore workflow.
package backupflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"bria/internal/backup"
)

var (
	ErrForbiddenPath    = errors.New("forbidden backup path")
	ErrForbiddenContent = errors.New("forbidden backup content")
	ErrInvalidLayout    = errors.New("invalid backup layout")
	ErrInvalidReceipt   = errors.New("invalid activation receipt")
)

// Layout names the exact product-state roots eligible for backup.
type Layout struct {
	Settings            string
	Computers           string
	Sessions            string
	SessionSidecars     []string
	UndeliveredMessages string
	TextHistory         string
	HarnessRules        string
	HarnessSettings     string
	HarnessChecks       string
	HarnessState        string
}

// ProductionSources binds the canonical semantic backup classes to exact
// production snapshots. UndeliveredMessagesExportPath must name a filtered
// export, never the raw message journal containing already delivered records.
type ProductionSources struct {
	StatePath                     string
	SessionSidecarPaths           []string
	ComputerCatalogExportPath     string
	UndeliveredMessagesExportPath string
	TextHistoryExportPath         string
	HarnessRulesPath              string
	HarnessSettingsPath           string
	HarnessChecksPath             string
	HarnessStatePath              string
}

// ProductionLayout resolves exact live and exported sources under a common
// source root. The Bria settings sidecar is derived from StatePath.
func ProductionLayout(sources ProductionSources) (string, Layout, error) {
	settingsPath := sources.StatePath + ".settings.json"
	named := []struct {
		name string
		path string
	}{
		{name: "state", path: sources.StatePath},
		{name: "settings", path: settingsPath},
		{name: "computer catalog export", path: sources.ComputerCatalogExportPath},
		{name: "undelivered messages export", path: sources.UndeliveredMessagesExportPath},
		{name: "text history export", path: sources.TextHistoryExportPath},
		{name: "Harness rules", path: sources.HarnessRulesPath},
		{name: "Harness settings", path: sources.HarnessSettingsPath},
		{name: "Harness checks", path: sources.HarnessChecksPath},
		{name: "Harness state", path: sources.HarnessStatePath},
	}
	for index, sidecar := range sources.SessionSidecarPaths {
		named = append(named, struct {
			name string
			path string
		}{name: fmt.Sprintf("session sidecar %d", index+1), path: sidecar})
	}
	paths := make([]string, 0, len(named))
	for _, source := range named {
		if strings.TrimSpace(source.path) == "" || !filepath.IsAbs(source.path) {
			return "", Layout{}, fmt.Errorf("%w: %s path must be absolute", ErrInvalidLayout, source.name)
		}
		absolute, err := filepath.Abs(source.path)
		if err != nil {
			return "", Layout{}, fmt.Errorf("%w: resolve %s path: %v", ErrInvalidLayout, source.name, err)
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			return "", Layout{}, fmt.Errorf("%w: %s snapshot is unavailable: %v", ErrInvalidLayout, source.name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return "", Layout{}, fmt.Errorf("%w: %s snapshot is not a regular file or directory", ErrInvalidLayout, source.name)
		}
		paths = append(paths, absolute)
	}
	root, err := commonSourceRoot(paths)
	if err != nil {
		return "", Layout{}, err
	}
	relative := make([]string, len(paths))
	for index, sourcePath := range paths {
		relativePath, err := filepath.Rel(root, sourcePath)
		if err != nil || relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return "", Layout{}, fmt.Errorf("%w: source paths do not share a safe root", ErrInvalidLayout)
		}
		relative[index] = filepath.ToSlash(relativePath)
	}
	layout := Layout{
		Sessions:            relative[0],
		Settings:            relative[1],
		Computers:           relative[2],
		UndeliveredMessages: relative[3],
		TextHistory:         relative[4],
		HarnessRules:        relative[5],
		HarnessSettings:     relative[6],
		HarnessChecks:       relative[7],
		HarnessState:        relative[8],
		SessionSidecars:     append([]string(nil), relative[9:]...),
	}
	if _, err := CanonicalPlan("production-layout-validation", layout); err != nil {
		return "", Layout{}, err
	}
	return root, layout, nil
}

func commonSourceRoot(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("%w: production sources are required", ErrInvalidLayout)
	}
	root := filepath.Dir(paths[0])
	for _, sourcePath := range paths[1:] {
		for !filepathContains(root, sourcePath) {
			parent := filepath.Dir(root)
			if parent == root {
				return "", fmt.Errorf("%w: production sources do not share a filesystem root", ErrInvalidLayout)
			}
			root = parent
		}
	}
	return root, nil
}

// CanonicalSnapshotLayout names the self-contained staging shape used by
// exporters and tests. Production code with live sidecars must use
// ProductionLayout instead of assuming these paths exist beside StatePath.
func CanonicalSnapshotLayout() Layout {
	return Layout{
		Settings: "settings/bria.json", Computers: "computers/catalog.json",
		Sessions: "sessions/state.json", UndeliveredMessages: "messages/undelivered.json",
		TextHistory: "history/text", HarnessRules: "harness/rules",
		HarnessSettings: "harness/settings", HarnessChecks: "harness/checks",
		HarnessState: "harness/state",
	}
}

func CanonicalPlan(computerID string, layout Layout) (backup.Plan, error) {
	computerID = strings.TrimSpace(computerID)
	if computerID == "" {
		return backup.Plan{}, fmt.Errorf("%w: computer id is required", ErrInvalidLayout)
	}
	includes := []backup.Include{
		{Path: layout.Settings, Class: backup.ClassSettings},
		{Path: layout.Computers, Class: backup.ClassComputers},
		{Path: layout.Sessions, Class: backup.ClassSessions},
		{Path: layout.UndeliveredMessages, Class: backup.ClassOutbox},
		{Path: layout.TextHistory, Class: backup.ClassHistory},
		{Path: layout.HarnessRules, Class: backup.ClassHarness},
		{Path: layout.HarnessSettings, Class: backup.ClassHarness},
		{Path: layout.HarnessChecks, Class: backup.ClassHarness},
		{Path: layout.HarnessState, Class: backup.ClassHarness},
	}
	for _, sidecar := range layout.SessionSidecars {
		includes = append(includes, backup.Include{Path: sidecar, Class: backup.ClassSessions})
	}
	seen := make(map[string]struct{}, len(includes))
	for _, include := range includes {
		if err := validateEligiblePath(include.Path); err != nil {
			return backup.Plan{}, err
		}
		if forbiddenPath(include.Path) {
			return backup.Plan{}, fmt.Errorf("%w: %q", ErrForbiddenPath, include.Path)
		}
		if _, exists := seen[include.Path]; exists {
			return backup.Plan{}, fmt.Errorf("%w: duplicate path %q", ErrInvalidLayout, include.Path)
		}
		seen[include.Path] = struct{}{}
	}
	sort.Slice(includes, func(i, j int) bool { return includes[i].Path < includes[j].Path })
	for left := range includes {
		for right := left + 1; right < len(includes); right++ {
			if pathContains(includes[left].Path, includes[right].Path) || pathContains(includes[right].Path, includes[left].Path) {
				return backup.Plan{}, fmt.Errorf("%w: overlapping paths %q and %q", ErrInvalidLayout, includes[left].Path, includes[right].Path)
			}
		}
	}
	return backup.Plan{ComputerID: computerID, Includes: includes}, nil
}

func validateEligiblePath(value string) error {
	if value == "" || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("%w: path %q is not canonical and relative", ErrInvalidLayout, value)
	}
	return nil
}

type Service struct {
	SourceRoot          string
	LatestPath          string
	RestoreCandidateDir string
	ComputerID          string
	Layout              Layout
}

type VerifiedCopy struct {
	Path        string
	Manifest    backup.Manifest
	Fingerprint string
}

type PreparedRestore struct {
	CandidateDir string
	ComputerID   string
	Fingerprint  string
	Manifest     backup.Manifest
}

type ActivationRequest struct {
	CandidateDir string
	ComputerID   string
	Fingerprint  string
}

type ActivationReceipt struct {
	ReceiptID    string
	CandidateDir string
	ComputerID   string
	Fingerprint  string
}

type Activator interface {
	Activate(context.Context, ActivationRequest) (ActivationReceipt, error)
}

func (service Service) CreateLatest() (VerifiedCopy, error) {
	plan, err := CanonicalPlan(service.ComputerID, service.Layout)
	if err != nil {
		return VerifiedCopy{}, err
	}
	if strings.TrimSpace(service.LatestPath) == "" {
		return VerifiedCopy{}, fmt.Errorf("%w: latest path is required", ErrInvalidLayout)
	}
	if err := validateOperationalPaths(service.SourceRoot, plan, service.LatestPath, service.LatestPath+".candidate"); err != nil {
		return VerifiedCopy{}, err
	}
	if err := rejectForbiddenEntries(service.SourceRoot, plan); err != nil {
		return VerifiedCopy{}, err
	}
	candidatePath := service.LatestPath + ".candidate"
	if err := removeStaleCandidate(candidatePath); err != nil {
		return VerifiedCopy{}, err
	}
	built, err := backup.BuildCandidate(service.SourceRoot, candidatePath, plan)
	if err != nil {
		return VerifiedCopy{}, err
	}
	if err := rejectForbiddenManifest(built); err != nil {
		_ = os.Remove(candidatePath)
		return VerifiedCopy{}, err
	}
	if err := verifyCandidateContent(candidatePath, built); err != nil {
		_ = os.Remove(candidatePath)
		return VerifiedCopy{}, err
	}
	manifest, err := backup.PromoteCandidate(candidatePath, service.LatestPath)
	if err != nil {
		return VerifiedCopy{}, err
	}
	verified, err := backup.Validate(service.LatestPath)
	if err != nil {
		return VerifiedCopy{}, fmt.Errorf("verify promoted latest backup: %w", err)
	}
	if !reflectManifest(manifest, verified) {
		return VerifiedCopy{}, errors.New("promoted backup manifest changed during verification")
	}
	fingerprint, err := manifestFingerprint(verified)
	if err != nil {
		return VerifiedCopy{}, err
	}
	return VerifiedCopy{Path: service.LatestPath, Manifest: verified, Fingerprint: fingerprint}, nil
}

func (service Service) PrepareRestore() (PreparedRestore, error) {
	if strings.TrimSpace(service.LatestPath) == "" || strings.TrimSpace(service.RestoreCandidateDir) == "" {
		return PreparedRestore{}, fmt.Errorf("%w: latest and restore candidate paths are required", ErrInvalidLayout)
	}
	plan, err := CanonicalPlan(service.ComputerID, service.Layout)
	if err != nil {
		return PreparedRestore{}, err
	}
	if err := validateOperationalPaths(service.SourceRoot, plan, service.LatestPath, service.RestoreCandidateDir); err != nil {
		return PreparedRestore{}, err
	}
	validated, err := backup.Validate(service.LatestPath)
	if err != nil {
		return PreparedRestore{}, err
	}
	if err := service.validateManifestPolicy(validated); err != nil {
		return PreparedRestore{}, err
	}
	manifest, err := backup.RestoreCandidate(service.LatestPath, service.RestoreCandidateDir)
	if err != nil {
		return PreparedRestore{}, err
	}
	if !reflectManifest(validated, manifest) {
		return PreparedRestore{}, errors.New("latest backup changed while preparing restore")
	}
	fingerprint, err := manifestFingerprint(manifest)
	if err != nil {
		return PreparedRestore{}, err
	}
	if err := verifyRestoredCandidate(service.RestoreCandidateDir, manifest); err != nil {
		return PreparedRestore{}, err
	}
	return PreparedRestore{
		CandidateDir: service.RestoreCandidateDir,
		ComputerID:   manifest.ComputerID,
		Fingerprint:  fingerprint,
		Manifest:     manifest,
	}, nil
}

func (service Service) ActivateRestore(ctx context.Context, prepared PreparedRestore, activator Activator) (ActivationReceipt, error) {
	if activator == nil {
		return ActivationReceipt{}, fmt.Errorf("%w: activator is required", ErrInvalidReceipt)
	}
	plan, err := CanonicalPlan(service.ComputerID, service.Layout)
	if err != nil {
		return ActivationReceipt{}, err
	}
	if err := validateOperationalPaths(service.SourceRoot, plan, service.LatestPath, service.RestoreCandidateDir); err != nil {
		return ActivationReceipt{}, err
	}
	expectedDir, err := filepath.Abs(service.RestoreCandidateDir)
	if err != nil {
		return ActivationReceipt{}, fmt.Errorf("resolve restore candidate: %w", err)
	}
	preparedDir, err := filepath.Abs(prepared.CandidateDir)
	if err != nil || expectedDir != preparedDir || prepared.ComputerID != strings.TrimSpace(service.ComputerID) {
		return ActivationReceipt{}, fmt.Errorf("%w: prepared restore does not match service", ErrInvalidReceipt)
	}
	preparedFingerprint, err := manifestFingerprint(prepared.Manifest)
	if err != nil || preparedFingerprint != prepared.Fingerprint || prepared.Manifest.ComputerID != prepared.ComputerID {
		return ActivationReceipt{}, fmt.Errorf("%w: prepared manifest identity mismatch", ErrInvalidReceipt)
	}
	latestManifest, err := backup.Validate(service.LatestPath)
	if err != nil {
		return ActivationReceipt{}, fmt.Errorf("verify latest before activation: %w", err)
	}
	if err := service.validateManifestPolicy(latestManifest); err != nil {
		return ActivationReceipt{}, err
	}
	latestFingerprint, err := manifestFingerprint(latestManifest)
	if err != nil || latestFingerprint != prepared.Fingerprint || !reflectManifest(latestManifest, prepared.Manifest) {
		return ActivationReceipt{}, fmt.Errorf("%w: latest backup changed after restore preparation", ErrInvalidReceipt)
	}
	if err := verifyRestoredCandidate(preparedDir, prepared.Manifest); err != nil {
		return ActivationReceipt{}, err
	}
	request := ActivationRequest{CandidateDir: preparedDir, ComputerID: prepared.ComputerID, Fingerprint: prepared.Fingerprint}
	receipt, err := activator.Activate(ctx, request)
	if err != nil {
		return ActivationReceipt{}, err
	}
	if strings.TrimSpace(receipt.ReceiptID) == "" || receipt.CandidateDir != request.CandidateDir || receipt.ComputerID != request.ComputerID || receipt.Fingerprint != request.Fingerprint {
		return ActivationReceipt{}, fmt.Errorf("%w: activation receipt does not match request", ErrInvalidReceipt)
	}
	return receipt, nil
}

var forbiddenNames = map[string]struct{}{
	"secret": {}, "secrets": {}, "credential": {}, "credentials": {},
	"auth": {}, "authorization": {}, "token": {}, "tokens": {}, "key": {}, "keys": {},
	"log": {}, "logs": {},
	"media": {}, "photo": {}, "photos": {}, "image": {}, "images": {},
	"video": {}, "videos": {}, "voice": {}, "audio": {},
	"file": {}, "files": {}, "artifact": {}, "artifacts": {},
	"attachment": {}, "attachments": {}, "archive": {}, "archives": {},
	"project": {}, "projects": {}, "workspace": {}, "workspaces": {},
	"workdir": {}, "workdirs": {},
	"bin": {}, "binary": {}, "binaries": {}, "executable": {}, "executables": {},
	"parakeet": {}, "model": {}, "models": {},
	"cache": {}, "caches": {}, "tmp": {}, "temp": {}, "temporary": {},
}

var forbiddenExtensions = map[string]struct{}{
	".key": {}, ".pem": {}, ".token": {}, ".log": {},
	".jpg": {}, ".jpeg": {}, ".png": {}, ".gif": {}, ".webp": {},
	".mp3": {}, ".wav": {}, ".ogg": {}, ".mp4": {}, ".mov": {},
	".zip": {}, ".tar": {}, ".gz": {}, ".bz2": {}, ".xz": {},
	".exe": {}, ".dll": {}, ".so": {}, ".dylib": {}, ".bin": {},
}

func rejectForbiddenEntries(root string, plan backup.Plan) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("%w: resolve source root: %v", ErrInvalidLayout, err)
	}
	for _, include := range plan.Includes {
		includePath := filepath.Join(rootAbsolute, filepath.FromSlash(include.Path))
		err := filepath.WalkDir(includePath, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(rootAbsolute, current)
			if err != nil {
				return err
			}
			if forbiddenPath(filepath.ToSlash(relative)) {
				return fmt.Errorf("%w: %q", ErrForbiddenPath, filepath.ToSlash(relative))
			}
			if !entry.IsDir() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
					return fmt.Errorf("%w: executable %q", ErrForbiddenPath, filepath.ToSlash(relative))
				}
				if info.Mode().IsRegular() {
					if err := scanFileForSecrets(current); err != nil {
						if errors.Is(err, ErrForbiddenContent) {
							return fmt.Errorf("%w: %q", ErrForbiddenContent, filepath.ToSlash(relative))
						}
						return fmt.Errorf("scan eligible content %q: %w", filepath.ToSlash(relative), err)
					}
				}
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, ErrForbiddenPath) || errors.Is(err, ErrForbiddenContent) {
				return err
			}
			return fmt.Errorf("scan eligible backup root %q: %w", include.Path, err)
		}
	}
	return nil
}

const (
	secretScanChunk   = 64 << 10
	secretScanOverlap = 64 << 10
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN(?: [A-Z0-9]+)? PRIVATE KEY-----`),
	regexp.MustCompile(`(?:^|[^0-9])[0-9]{6,12}:[A-Za-z0-9_-]{30,}(?:$|[^A-Za-z0-9_-])`),
	regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`(?i)(?:telegram[-_ ]?token|codex[-_ ]?auth(?:[-_ ]?token)?|claude[-_ ]?auth(?:[-_ ]?token)?|api[-_ ]?key|private[-_ ]?key|access[-_ ]?token|refresh[-_ ]?token|client[-_ ]?secret|password|passphrase|oauth|auth|secret)\s*["']?\s*[:=]\s*["']?[A-Za-z0-9._~+/@:-]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}\b`),
}

func scanFileForSecrets(filePath string) error {
	input, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer input.Close()

	chunk := make([]byte, secretScanChunk)
	var overlap []byte
	var utf8Remainder []byte
	for {
		read, readErr := input.Read(chunk)
		if read > 0 {
			current := chunk[:read]
			if bytes.IndexByte(current, 0) >= 0 {
				return ErrForbiddenContent
			}
			for _, value := range current {
				if value < 0x20 && value != '\n' && value != '\r' && value != '\t' {
					return ErrForbiddenContent
				}
			}
			utf8Chunk := append(append([]byte(nil), utf8Remainder...), current...)
			utf8Remainder = utf8Remainder[:0]
			if !utf8.Valid(utf8Chunk) {
				validEnd := -1
				for trailing := 1; trailing < utf8.UTFMax && trailing <= len(utf8Chunk); trailing++ {
					candidateEnd := len(utf8Chunk) - trailing
					if utf8.Valid(utf8Chunk[:candidateEnd]) && !utf8.FullRune(utf8Chunk[candidateEnd:]) {
						validEnd = candidateEnd
						break
					}
				}
				if validEnd < 0 {
					return ErrForbiddenContent
				}
				utf8Remainder = append(utf8Remainder, utf8Chunk[validEnd:]...)
			}

			window := append(append([]byte(nil), overlap...), current...)
			for _, pattern := range secretPatterns {
				if pattern.Match(window) {
					return ErrForbiddenContent
				}
			}
			keep := secretScanOverlap
			if keep > len(window) {
				keep = len(window)
			}
			overlap = append(overlap[:0], window[len(window)-keep:]...)
		}
		if errors.Is(readErr, io.EOF) {
			if len(utf8Remainder) != 0 {
				return ErrForbiddenContent
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func forbiddenPath(relative string) bool {
	for _, component := range strings.Split(relative, "/") {
		lower := strings.ToLower(component)
		base := strings.TrimSuffix(lower, path.Ext(lower))
		if _, forbidden := forbiddenNames[lower]; forbidden {
			return true
		}
		if _, forbidden := forbiddenNames[base]; forbidden {
			return true
		}
		for _, word := range strings.FieldsFunc(base, func(character rune) bool {
			return !unicode.IsLetter(character) && !unicode.IsNumber(character)
		}) {
			if _, forbidden := forbiddenNames[word]; forbidden {
				return true
			}
		}
		if _, forbidden := forbiddenExtensions[path.Ext(lower)]; forbidden {
			return true
		}
		if lower == ".env" || strings.HasPrefix(lower, ".env.") || lower == "id_rsa" || lower == "id_ed25519" {
			return true
		}
	}
	return false
}

func rejectForbiddenManifest(manifest backup.Manifest) error {
	for _, entry := range manifest.Files {
		if forbiddenPath(entry.Path) {
			return fmt.Errorf("%w: %q", ErrForbiddenPath, entry.Path)
		}
	}
	return nil
}

func verifyCandidateContent(candidatePath string, manifest backup.Manifest) error {
	parent, err := os.MkdirTemp(filepath.Dir(candidatePath), ".backup-content-check-")
	if err != nil {
		return fmt.Errorf("create backup content verification directory: %w", err)
	}
	defer os.RemoveAll(parent)
	restored := filepath.Join(parent, "restored")
	verified, err := backup.RestoreCandidate(candidatePath, restored)
	if err != nil {
		return fmt.Errorf("restore backup for content verification: %w", err)
	}
	if !reflectManifest(manifest, verified) {
		return errors.New("backup manifest changed during content verification")
	}
	for _, entry := range manifest.Files {
		if err := scanFileForSecrets(filepath.Join(restored, filepath.FromSlash(entry.Path))); err != nil {
			if errors.Is(err, ErrForbiddenContent) {
				return fmt.Errorf("%w: %q", ErrForbiddenContent, entry.Path)
			}
			return fmt.Errorf("scan archived content %q: %w", entry.Path, err)
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func validateOperationalPaths(sourceRoot string, plan backup.Plan, operationalPaths ...string) error {
	rootAbsolute, err := filepath.Abs(sourceRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve source root: %v", ErrInvalidLayout, err)
	}
	eligible := make([]string, 0, len(plan.Includes))
	for _, include := range plan.Includes {
		eligible = append(eligible, filepath.Join(rootAbsolute, filepath.FromSlash(include.Path)))
	}
	for _, operational := range operationalPaths {
		operationalAbsolute, err := filepath.Abs(operational)
		if err != nil {
			return fmt.Errorf("%w: resolve operational path: %v", ErrInvalidLayout, err)
		}
		for _, includeAbsolute := range eligible {
			if filepathContains(includeAbsolute, operationalAbsolute) || filepathContains(operationalAbsolute, includeAbsolute) {
				return fmt.Errorf("%w: operational path overlaps eligible state", ErrInvalidLayout)
			}
		}
	}
	return nil
}

func filepathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func removeStaleCandidate(candidatePath string) error {
	info, err := os.Lstat(candidatePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect stale backup candidate: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("stale backup candidate is not a regular file")
	}
	if err := os.Remove(candidatePath); err != nil {
		return fmt.Errorf("remove stale backup candidate: %w", err)
	}
	return nil
}

func manifestFingerprint(manifest backup.Manifest) (string, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode backup manifest fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func reflectManifest(left, right backup.Manifest) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func (service Service) validateManifestPolicy(manifest backup.Manifest) error {
	plan, err := CanonicalPlan(service.ComputerID, service.Layout)
	if err != nil {
		return err
	}
	if manifest.ComputerID != plan.ComputerID || len(manifest.Excludes) != 0 || len(manifest.Includes) != len(plan.Includes) {
		return fmt.Errorf("%w: backup does not match canonical product policy", backup.ErrInvalidBackup)
	}
	for index := range plan.Includes {
		if manifest.Includes[index] != plan.Includes[index] {
			return fmt.Errorf("%w: backup does not match canonical product policy", backup.ErrInvalidBackup)
		}
	}
	return rejectForbiddenManifest(manifest)
}

func verifyRestoredCandidate(root string, manifest backup.Manifest) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("verify restore candidate path: %w", err)
	}
	rootInfo, err := os.Lstat(rootAbsolute)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("restore candidate is not a regular directory")
	}
	expected := make(map[string]backup.File, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
		fullPath := filepath.Join(rootAbsolute, filepath.FromSlash(entry.Path))
		info, err := os.Lstat(fullPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != entry.Size {
			return fmt.Errorf("restored payload %q changed before activation", entry.Path)
		}
		digest, err := digestFile(fullPath)
		if err != nil || digest != entry.SHA256 {
			return fmt.Errorf("restored payload %q failed checksum before activation", entry.Path)
		}
	}
	seen := 0
	err = filepath.WalkDir(rootAbsolute, func(current string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(rootAbsolute, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, ok := expected[relative]; !ok {
			return fmt.Errorf("unexpected restored payload %q", relative)
		}
		seen++
		return nil
	})
	if err != nil {
		return fmt.Errorf("restore candidate file set changed before activation: %w", err)
	}
	if seen != len(expected) {
		return errors.New("restore candidate file set changed before activation")
	}
	return nil
}

func digestFile(filePath string) (string, error) {
	input, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer input.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
