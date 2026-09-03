// Package backup creates and validates self-contained, allowlist-only backups.
// It deliberately does not choose a schedule, remote store, or encryption policy.
package backup

import (
	"archive/tar"
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
	"sort"
	"strings"
)

const (
	FormatVersion = 1
	manifestName  = "manifest.json"
	filesPrefix   = "files/"
	maxManifest   = 16 << 20
)

var (
	ErrInvalidPlan     = errors.New("invalid backup plan")
	ErrInvalidBackup   = errors.New("invalid backup")
	ErrCandidateExists = errors.New("backup candidate already exists")
)

// Class is an allowed product-state class. There is intentionally no generic
// class: callers must place every selected path in the product allowlist.
type Class string

const (
	ClassSettings  Class = "settings"
	ClassComputers Class = "computers"
	ClassSessions  Class = "sessions"
	ClassOutbox    Class = "outbox"
	ClassHistory   Class = "history"
	ClassHarness   Class = "harness"
)

// Include selects a regular file or directory tree relative to the source
// root. Directory trees are recursive; symlinks and non-regular files fail.
type Include struct {
	Path  string `json:"path"`
	Class Class  `json:"class"`
}

// Exclude removes an exact path and its descendants from an included tree.
// Reason makes sensitive exclusions auditable in the backup manifest.
type Exclude struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Plan is deny-by-default: only Includes are considered and every exception
// must be an explicit Exclude.
type Plan struct {
	ComputerID string    `json:"computer_id"`
	Includes   []Include `json:"includes"`
	Excludes   []Exclude `json:"excludes,omitempty"`
}

type File struct {
	Path   string `json:"path"`
	Class  Class  `json:"class"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is stored inside every backup and covers both its allowlist policy
// and every payload byte.
type Manifest struct {
	FormatVersion int       `json:"format_version"`
	ComputerID    string    `json:"computer_id"`
	Includes      []Include `json:"includes"`
	Excludes      []Exclude `json:"excludes,omitempty"`
	Files         []File    `json:"files"`
}

// BuildCandidate writes a complete candidate without changing the confirmed
// backup. candidatePath must not exist.
func BuildCandidate(sourceRoot, candidatePath string, plan Plan) (manifest Manifest, returnErr error) {
	validated, err := validatePlan(plan)
	if err != nil {
		return Manifest{}, err
	}
	root, err := existingDirectory(sourceRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: source root: %v", ErrInvalidPlan, err)
	}
	files, err := collect(root, validated)
	if err != nil {
		return Manifest{}, err
	}
	manifest = Manifest{
		FormatVersion: FormatVersion,
		ComputerID:    validated.ComputerID,
		Includes:      append([]Include(nil), validated.Includes...),
		Excludes:      append([]Exclude(nil), validated.Excludes...),
		Files:         files,
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}

	if err := os.MkdirAll(filepath.Dir(candidatePath), 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create candidate parent: %w", err)
	}
	output, err := os.OpenFile(candidatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return Manifest{}, ErrCandidateExists
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("create backup candidate: %w", err)
	}
	defer func() {
		_ = output.Close()
		if returnErr != nil {
			_ = os.Remove(candidatePath)
		}
	}()

	writer := tar.NewWriter(output)
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("marshal backup manifest: %w", err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifestBytes)), Typeflag: tar.TypeReg}); err != nil {
		return Manifest{}, fmt.Errorf("write backup manifest header: %w", err)
	}
	if _, err := writer.Write(manifestBytes); err != nil {
		return Manifest{}, fmt.Errorf("write backup manifest: %w", err)
	}
	for _, entry := range manifest.Files {
		if err := writePayload(writer, root, entry); err != nil {
			return Manifest{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return Manifest{}, fmt.Errorf("finish backup candidate: %w", err)
	}
	if err := output.Sync(); err != nil {
		return Manifest{}, fmt.Errorf("sync backup candidate: %w", err)
	}
	if err := output.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close backup candidate: %w", err)
	}
	return manifest, nil
}

// Validate rereads the complete archive and verifies its format, allowlist,
// exact payload set, sizes, and SHA-256 checksums.
func Validate(archivePath string) (Manifest, error) {
	info, err := os.Lstat(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: stat archive: %v", ErrInvalidBackup, err)
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, fmt.Errorf("%w: archive is not a regular file", ErrInvalidBackup)
	}
	input, err := os.Open(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: open archive: %v", ErrInvalidBackup, err)
	}
	defer input.Close()

	reader := tar.NewReader(input)
	header, err := reader.Next()
	if err != nil || header.Name != manifestName || !regularHeader(header) || header.Size > maxManifest {
		return Manifest{}, fmt.Errorf("%w: missing or invalid manifest", ErrInvalidBackup)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(reader, header.Size))
	if err != nil || int64(len(manifestBytes)) != header.Size {
		return Manifest{}, fmt.Errorf("%w: read manifest", ErrInvalidBackup)
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalidBackup, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, fmt.Errorf("%w: trailing manifest data", ErrInvalidBackup)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || string(canonical) != string(manifestBytes) {
		return Manifest{}, fmt.Errorf("%w: manifest is not canonical", ErrInvalidBackup)
	}
	expected := make(map[string]File, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
	}
	seen := make(map[string]struct{}, len(expected))
	for {
		header, err = reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: read archive: %v", ErrInvalidBackup, err)
		}
		if !regularHeader(header) || !strings.HasPrefix(header.Name, filesPrefix) {
			return Manifest{}, fmt.Errorf("%w: unexpected archive member %q", ErrInvalidBackup, header.Name)
		}
		name := strings.TrimPrefix(header.Name, filesPrefix)
		entry, ok := expected[name]
		if !ok {
			return Manifest{}, fmt.Errorf("%w: unlisted payload %q", ErrInvalidBackup, name)
		}
		if _, duplicate := seen[name]; duplicate {
			return Manifest{}, fmt.Errorf("%w: duplicate payload %q", ErrInvalidBackup, name)
		}
		if header.Size != entry.Size {
			return Manifest{}, fmt.Errorf("%w: size mismatch for %q", ErrInvalidBackup, name)
		}
		hash := sha256.New()
		written, copyErr := io.Copy(hash, reader)
		if copyErr != nil || written != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return Manifest{}, fmt.Errorf("%w: checksum mismatch for %q", ErrInvalidBackup, name)
		}
		seen[name] = struct{}{}
	}
	if len(seen) != len(expected) {
		return Manifest{}, fmt.Errorf("%w: archive payload is incomplete", ErrInvalidBackup)
	}
	return manifest, nil
}

// PromoteCandidate verifies candidatePath before atomically renaming it over
// latestPath. A rejected or unpromotable candidate is removed; an existing
// confirmed backup is never removed first.
func PromoteCandidate(candidatePath, latestPath string) (Manifest, error) {
	candidateAbsolute, candidateErr := filepath.Abs(candidatePath)
	latestAbsolute, latestErr := filepath.Abs(latestPath)
	if candidateErr != nil || latestErr != nil || candidateAbsolute == latestAbsolute {
		return Manifest{}, errors.New("backup candidate and latest paths must be distinct")
	}
	manifest, err := Validate(candidatePath)
	if err != nil {
		_ = os.Remove(candidatePath)
		return Manifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(latestPath), 0o700); err != nil {
		_ = os.Remove(candidatePath)
		return Manifest{}, fmt.Errorf("create latest backup parent: %w", err)
	}
	if err := os.Rename(candidatePath, latestPath); err != nil {
		_ = os.Remove(candidatePath)
		return Manifest{}, fmt.Errorf("atomically promote backup candidate: %w", err)
	}
	if err := syncDirectory(filepath.Dir(latestPath)); err != nil {
		return Manifest{}, fmt.Errorf("sync latest backup directory: %w", err)
	}
	return manifest, nil
}

// RestoreCandidate extracts a validated archive into a new candidate
// directory, rereads every restored file, and publishes the directory only
// after that verification. It never overwrites live state.
func RestoreCandidate(archivePath, candidateDir string) (manifest Manifest, returnErr error) {
	manifest, err := Validate(archivePath)
	if err != nil {
		return Manifest{}, err
	}
	if _, err := os.Lstat(candidateDir); err == nil {
		return Manifest{}, ErrCandidateExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, fmt.Errorf("stat restore candidate: %w", err)
	}
	parent := filepath.Dir(candidateDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create restore parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".bria-restore-")
	if err != nil {
		return Manifest{}, fmt.Errorf("create restore temporary directory: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(temporary)
			_ = os.RemoveAll(candidateDir)
		}
	}()
	if err := extract(archivePath, temporary, manifest); err != nil {
		return Manifest{}, err
	}
	if err := validateRestored(temporary, manifest); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(temporary, candidateDir); err != nil {
		return Manifest{}, fmt.Errorf("publish restore candidate: %w", err)
	}
	temporary = candidateDir
	if err := validateRestored(candidateDir, manifest); err != nil {
		return Manifest{}, err
	}
	if err := syncDirectory(parent); err != nil {
		return Manifest{}, fmt.Errorf("sync restore parent: %w", err)
	}
	return manifest, nil
}

func validatePlan(plan Plan) (Plan, error) {
	plan.Includes = append([]Include(nil), plan.Includes...)
	plan.Excludes = append([]Exclude(nil), plan.Excludes...)
	plan.ComputerID = strings.TrimSpace(plan.ComputerID)
	if plan.ComputerID == "" || len(plan.Includes) == 0 {
		return Plan{}, fmt.Errorf("%w: computer id and includes are required", ErrInvalidPlan)
	}
	includePaths := make(map[string]struct{}, len(plan.Includes))
	for index := range plan.Includes {
		clean, err := cleanRelative(plan.Includes[index].Path)
		if err != nil || !allowedClass(plan.Includes[index].Class) {
			return Plan{}, fmt.Errorf("%w: invalid include %q", ErrInvalidPlan, plan.Includes[index].Path)
		}
		plan.Includes[index].Path = clean
		if _, exists := includePaths[clean]; exists {
			return Plan{}, fmt.Errorf("%w: duplicate include %q", ErrInvalidPlan, clean)
		}
		includePaths[clean] = struct{}{}
	}
	for left := range plan.Includes {
		for right := left + 1; right < len(plan.Includes); right++ {
			if containsPath(plan.Includes[left].Path, plan.Includes[right].Path) || containsPath(plan.Includes[right].Path, plan.Includes[left].Path) {
				return Plan{}, fmt.Errorf("%w: overlapping includes", ErrInvalidPlan)
			}
		}
	}
	excludePaths := make(map[string]struct{}, len(plan.Excludes))
	for index := range plan.Excludes {
		clean, err := cleanRelative(plan.Excludes[index].Path)
		if err != nil || strings.TrimSpace(plan.Excludes[index].Reason) == "" {
			return Plan{}, fmt.Errorf("%w: invalid exclusion %q", ErrInvalidPlan, plan.Excludes[index].Path)
		}
		plan.Excludes[index].Path = clean
		inside := false
		for _, include := range plan.Includes {
			if include.Path != clean && containsPath(include.Path, clean) {
				inside = true
				break
			}
		}
		if !inside {
			return Plan{}, fmt.Errorf("%w: exclusion %q is outside includes", ErrInvalidPlan, clean)
		}
		if _, exists := excludePaths[clean]; exists {
			return Plan{}, fmt.Errorf("%w: duplicate exclusion %q", ErrInvalidPlan, clean)
		}
		excludePaths[clean] = struct{}{}
	}
	sort.Slice(plan.Includes, func(i, j int) bool { return plan.Includes[i].Path < plan.Includes[j].Path })
	sort.Slice(plan.Excludes, func(i, j int) bool { return plan.Excludes[i].Path < plan.Excludes[j].Path })
	return plan, nil
}

func collect(root string, plan Plan) ([]File, error) {
	var files []File
	for _, include := range plan.Includes {
		full := filepath.Join(root, filepath.FromSlash(include.Path))
		info, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("collect include %q: %w", include.Path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: symlink include %q", ErrInvalidPlan, include.Path)
		}
		err = filepath.WalkDir(full, func(current string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if excluded(relative, plan.Excludes) {
				if item.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := item.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink %q is forbidden", relative)
			}
			if item.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("non-regular file %q is forbidden", relative)
			}
			digest, err := digestFile(current)
			if err != nil {
				return err
			}
			files = append(files, File{Path: relative, Class: include.Class, Size: info.Size(), SHA256: digest})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("collect include %q: %w", include.Path, err)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func writePayload(writer *tar.Writer, root string, entry File) error {
	filePath := filepath.Join(root, filepath.FromSlash(entry.Path))
	pathInfo, err := os.Lstat(filePath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("payload %q changed while backing up", entry.Path)
	}
	input, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open payload %q: %w", entry.Path, err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
		return fmt.Errorf("payload %q changed while backing up", entry.Path)
	}
	if err := writer.WriteHeader(&tar.Header{Name: filesPrefix + entry.Path, Mode: 0o600, Size: entry.Size, Typeflag: tar.TypeReg}); err != nil {
		return fmt.Errorf("write payload header %q: %w", entry.Path, err)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(writer, hash), input)
	if err != nil || written != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return fmt.Errorf("payload %q changed while backing up", entry.Path)
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.FormatVersion != FormatVersion {
		return fmt.Errorf("%w: unsupported format version %d", ErrInvalidBackup, manifest.FormatVersion)
	}
	plan, err := validatePlan(Plan{ComputerID: manifest.ComputerID, Includes: manifest.Includes, Excludes: manifest.Excludes})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBackup, err)
	}
	if !reflectIncludes(plan.Includes, manifest.Includes) || !reflectExcludes(plan.Excludes, manifest.Excludes) {
		return fmt.Errorf("%w: manifest policy is not canonical", ErrInvalidBackup)
	}
	previous := ""
	for _, entry := range manifest.Files {
		clean, cleanErr := cleanRelative(entry.Path)
		class, ok := classFor(clean, plan.Includes)
		if cleanErr != nil || !ok || class != entry.Class || excluded(clean, plan.Excludes) || entry.Size < 0 || len(entry.SHA256) != sha256.Size*2 {
			return fmt.Errorf("%w: invalid file entry %q", ErrInvalidBackup, entry.Path)
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			return fmt.Errorf("%w: invalid checksum for %q", ErrInvalidBackup, entry.Path)
		}
		if previous >= clean {
			return fmt.Errorf("%w: file entries are not unique and sorted", ErrInvalidBackup)
		}
		previous = clean
	}
	return nil
}

func extract(archivePath, destination string, manifest Manifest) error {
	input, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open backup for restore: %w", err)
	}
	defer input.Close()
	reader := tar.NewReader(input)
	if _, err := reader.Next(); err != nil {
		return fmt.Errorf("read backup manifest for restore: %w", err)
	}
	expected := make(map[string]File, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read backup payload for restore: %w", err)
		}
		name := strings.TrimPrefix(header.Name, filesPrefix)
		entry, ok := expected[name]
		if !ok || !regularHeader(header) {
			return fmt.Errorf("%w: unexpected restore payload %q", ErrInvalidBackup, header.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create restore directory: %w", err)
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create restored file %q: %w", name, err)
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, hash), reader)
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || written != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("%w: restored payload %q failed verification", ErrInvalidBackup, name)
		}
	}
	return nil
}

func validateRestored(root string, manifest Manifest) error {
	expected := make(map[string]File, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
		full := filepath.Join(root, filepath.FromSlash(entry.Path))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			return fmt.Errorf("%w: restored file %q missing or changed", ErrInvalidBackup, entry.Path)
		}
		digest, err := digestFile(full)
		if err != nil || digest != entry.SHA256 {
			return fmt.Errorf("%w: restored file %q checksum mismatch", ErrInvalidBackup, entry.Path)
		}
	}
	count := 0
	err := filepath.WalkDir(root, func(current string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, ok := expected[relative]; !ok {
			return fmt.Errorf("unexpected restored file %q", relative)
		}
		count++
		return nil
	})
	if err != nil || count != len(expected) {
		return fmt.Errorf("%w: restored file set differs: %v", ErrInvalidBackup, err)
	}
	return nil
}

func existingDirectory(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("must be an existing non-symlink directory")
	}
	return absolute, nil
}

func digestFile(name string) (string, error) {
	input, err := os.Open(name)
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

func cleanRelative(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") || strings.ContainsRune(value, 0) || path.IsAbs(value) {
		return "", errors.New("path must be portable and relative")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", errors.New("path is not canonical")
	}
	return clean, nil
}

func containsPath(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func excluded(candidate string, exclusions []Exclude) bool {
	for _, exclusion := range exclusions {
		if containsPath(exclusion.Path, candidate) {
			return true
		}
	}
	return false
}

func classFor(candidate string, includes []Include) (Class, bool) {
	for _, include := range includes {
		if containsPath(include.Path, candidate) {
			return include.Class, true
		}
	}
	return "", false
}

func allowedClass(class Class) bool {
	switch class {
	case ClassSettings, ClassComputers, ClassSessions, ClassOutbox, ClassHistory, ClassHarness:
		return true
	default:
		return false
	}
}

func regularHeader(header *tar.Header) bool {
	return header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA
}

func reflectIncludes(left, right []Include) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func reflectExcludes(left, right []Exclude) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
