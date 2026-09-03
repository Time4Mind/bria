// Package containerpreflight verifies the immutable, externally mounted
// provider artifacts required by one Docker role before Bria starts.
package containerpreflight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"bria/internal/config"
)

const (
	maxLockBytes      = 1 << 20
	maxExecutableSize = 1 << 30
	maxModelSize      = 64 << 30
	maxIdentityBytes  = 512
	maxVersionBytes   = 4096
	versionTimeout    = 10 * time.Second
)

type Options struct {
	ConfigPath   string
	LockPath     string
	ExpectedRole string
	ProviderRoot string
	ModelRoot    string
}

type lockDocument struct {
	SchemaVersion int        `json:"schema_version"`
	Role          string     `json:"role"`
	Artifacts     []artifact `json:"artifacts"`
}

type artifact struct {
	Name         string        `json:"name"`
	Kind         string        `json:"kind"`
	Path         string        `json:"path"`
	Size         int64         `json:"size"`
	SHA256       string        `json:"sha256"`
	Identity     string        `json:"identity"`
	VersionProbe *versionProbe `json:"version_probe,omitempty"`
}

type versionProbe struct {
	Argv        []string `json:"argv"`
	ExactStdout string   `json:"exact_stdout"`
}

type requirement struct {
	name string
	kind string
	path string
	root string
}

func Verify(ctx context.Context, options Options) error {
	if ctx == nil || !absolute(options.ConfigPath) || !absolute(options.LockPath) ||
		!absolute(options.ProviderRoot) || !absolute(options.ModelRoot) {
		return errors.New("container preflight paths must be absolute")
	}
	configuration, err := config.LoadFile(options.ConfigPath)
	if err != nil {
		return fmt.Errorf("load container configuration: %w", err)
	}
	if configuration.IsLegacy() {
		return errors.New("container preflight requires an explicit versioned role configuration")
	}
	role := string(configuration.EffectiveRole())
	if options.ExpectedRole != role {
		return fmt.Errorf("container role %q does not match configuration role %q", options.ExpectedRole, role)
	}
	document, err := readLock(options.LockPath)
	if err != nil {
		return err
	}
	if document.SchemaVersion != 1 || document.Role != role || document.Artifacts == nil {
		return errors.New("provider lock identity does not match the configured role")
	}
	required := requirements(configuration, options)
	if len(document.Artifacts) != len(required) {
		return errors.New("provider lock artifact set does not exactly match configuration")
	}
	seen := make(map[string]struct{}, len(document.Artifacts))
	for _, candidate := range document.Artifacts {
		expected, ok := required[candidate.Name]
		if !ok {
			return fmt.Errorf("provider lock contains unexpected artifact %q", candidate.Name)
		}
		if _, duplicate := seen[candidate.Name]; duplicate {
			return fmt.Errorf("provider lock repeats artifact %q", candidate.Name)
		}
		seen[candidate.Name] = struct{}{}
		if err := verifyArtifact(ctx, candidate, expected); err != nil {
			return fmt.Errorf("artifact %q: %w", candidate.Name, err)
		}
	}
	return nil
}

func requirements(configuration config.Config, options Options) map[string]requirement {
	result := make(map[string]requirement)
	if !configuration.Executes() {
		return result
	}
	for _, name := range []string{"codex", "claude"} {
		provider, ok := configuration.Providers[name]
		if ok && provider.Command != nil {
			result[name] = requirement{name: name, kind: "executable", path: provider.Command.Exec, root: options.ProviderRoot}
		}
	}
	result["parakeet"] = requirement{name: "parakeet", kind: "executable", path: configuration.Parakeet.Executable, root: options.ProviderRoot}
	result["parakeet_model"] = requirement{name: "parakeet_model", kind: "data", path: configuration.Parakeet.ModelPath, root: options.ModelRoot}
	return result
}

func readLock(path string) (lockDocument, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		before.Size() <= 0 || before.Size() > maxLockBytes || before.Mode().Perm()&0o222 != 0 {
		return lockDocument{}, errors.New("provider lock must be a bounded, non-symlink, read-only regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return lockDocument{}, errors.New("open provider lock")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return lockDocument{}, errors.New("provider lock changed during secure open")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxLockBytes+1))
	if err != nil || len(contents) > maxLockBytes {
		return lockDocument{}, errors.New("read bounded provider lock")
	}
	if err := rejectDuplicateKeys(contents); err != nil {
		return lockDocument{}, fmt.Errorf("provider lock JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var document lockDocument
	if err := decoder.Decode(&document); err != nil {
		return lockDocument{}, errors.New("provider lock is invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return lockDocument{}, errors.New("provider lock contains trailing JSON")
	}
	return document, nil
}

func verifyArtifact(ctx context.Context, candidate artifact, expected requirement) error {
	if candidate.Name != expected.name || candidate.Kind != expected.kind || candidate.Path != expected.path ||
		len(candidate.Identity) == 0 || len(candidate.Identity) > maxIdentityBytes || strings.TrimSpace(candidate.Identity) != candidate.Identity {
		return errors.New("metadata does not match the exact configured artifact")
	}
	maximum := int64(maxExecutableSize)
	if candidate.Kind == "data" {
		maximum = maxModelSize
	}
	if candidate.Size <= 0 || candidate.Size > maximum || len(candidate.SHA256) != sha256.Size*2 {
		return errors.New("size or SHA-256 constraint is invalid")
	}
	if _, err := hex.DecodeString(candidate.SHA256); err != nil || candidate.SHA256 != strings.ToLower(candidate.SHA256) {
		return errors.New("SHA-256 constraint is invalid")
	}
	if err := validatePath(candidate.Path, expected.root); err != nil {
		return err
	}
	before, err := os.Lstat(candidate.Path)
	if err != nil {
		return errors.New("artifact is not mounted")
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return errors.New("artifact must not be a symlink")
	}
	if !before.Mode().IsRegular() || before.Size() != candidate.Size {
		return errors.New("artifact integrity size mismatch")
	}
	if before.Mode().Perm()&0o222 != 0 {
		return errors.New("artifact must be read-only")
	}
	if candidate.Kind == "executable" && before.Mode().Perm()&0o111 == 0 {
		return errors.New("artifact is not executable")
	}
	file, err := os.Open(candidate.Path)
	if err != nil {
		return errors.New("open artifact")
	}
	digest := sha256.New()
	written, copyErr := io.Copy(digest, io.LimitReader(file, candidate.Size+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	if copyErr != nil || statErr != nil || closeErr != nil || written != candidate.Size || !os.SameFile(before, after) || after.Size() != candidate.Size ||
		hex.EncodeToString(digest.Sum(nil)) != candidate.SHA256 {
		return errors.New("artifact integrity verification failed")
	}
	if candidate.Kind == "data" {
		if candidate.VersionProbe != nil {
			return errors.New("data artifact must not define a version probe")
		}
		return nil
	}
	return verifyVersion(ctx, candidate)
}

func verifyVersion(ctx context.Context, candidate artifact) error {
	probe := candidate.VersionProbe
	if probe == nil || len(probe.Argv) != 1 ||
		(probe.Argv[0] != "--version" && probe.Argv[0] != "version") ||
		probe.ExactStdout == "" || len(probe.ExactStdout) > maxVersionBytes {
		return errors.New("executable requires a bounded exact version probe")
	}
	for _, argument := range probe.Argv {
		if argument == "" || len(argument) > 256 || strings.ContainsRune(argument, 0) {
			return errors.New("version probe argument is invalid")
		}
	}
	probeContext, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	command := exec.CommandContext(probeContext, candidate.Path, probe.Argv...)
	command.Env = []string{"HOME=/nonexistent", "PATH=/nonexistent", "NO_COLOR=1"}
	command.Dir = "/"
	var stdout boundedBuffer
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || stdout.exceeded || stdout.String() != probe.ExactStdout {
		return errors.New("exact executable version verification failed")
	}
	return nil
}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := maxVersionBytes - buffer.Len()
	if remaining < len(value) {
		if remaining > 0 {
			_, _ = buffer.Buffer.Write(value[:remaining])
		}
		buffer.exceeded = true
		return len(value), nil
	}
	return buffer.Buffer.Write(value)
}

func validatePath(path, root string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == "" || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("artifact path is outside its fixed mount root")
	}
	if filepath.Dir(path) != root {
		return errors.New("artifact must be a direct file mount in its fixed root")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm()&0o222 != 0 {
		return errors.New("artifact mount root is unavailable or symlinked")
	}
	candidate := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		candidate = filepath.Join(candidate, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			return errors.New("artifact path component is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact path must not contain a symlink")
		}
	}
	return nil
}

func absolute(path string) bool {
	return path != "" && filepath.IsAbs(path) && !strings.ContainsRune(path, 0)
}

func rejectDuplicateKeys(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate key %q", key)
				}
				seen[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	return visit()
}
