package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// LoadFile decodes one configuration file and rejects collisions between that
// file, the durable state file, and the token secret file when their paths can
// be resolved locally.
func LoadFile(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("configuration path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve configuration path: %w", err)
	}
	before, err := os.Lstat(absolute)
	if err != nil {
		return Config{}, fmt.Errorf("inspect configuration file: %w", err)
	}
	if err := validateConfigFileInfo(before); err != nil {
		return Config{}, err
	}

	file, err := os.Open(absolute)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return Config{}, fmt.Errorf("verify configuration file: %w", err)
	}
	if !os.SameFile(before, after) {
		return Config{}, errors.New("configuration file changed during secure open")
	}
	if err := validateConfigFileInfo(after); err != nil {
		return Config{}, err
	}

	configuration, err := Decode(file)
	if err != nil {
		return Config{}, err
	}
	configuration.sourcePath = absolute
	if err := configuration.validateSourcePathCollisions(); err != nil {
		return Config{}, err
	}
	return configuration, nil
}

func validateConfigFileInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("configuration file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("configuration file must be regular")
	}
	permissions := info.Mode().Perm()
	if permissions&0o600 != 0o600 {
		return errors.New("configuration file owner must have read and write access")
	}
	if runtime.GOOS != "windows" {
		if permissions&0o022 != 0 {
			return errors.New("configuration file must not be writable by group or others")
		}
	}
	return nil
}

func (config Config) validateSourcePathCollisions() error {
	if config.sourcePath == "" {
		return nil
	}
	if knownSamePath(config.sourcePath, config.StatePath) {
		return errors.New("configuration path and state_path must differ")
	}
	if config.TelegramToken.SecretFile != "" &&
		knownSamePath(config.sourcePath, config.TelegramToken.SecretFile) {
		return errors.New("configuration path and telegram_token secret_file must differ")
	}
	if knownSamePath(config.sourcePath, config.CallbackKey.SecretFile) {
		return errors.New("configuration path and callback_key secret_file must differ")
	}
	if config.Version != 0 {
		references := config.PathReferences()
		names := make([]string, 0, len(references))
		for name := range references {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			path := references[name]
			if knownSamePath(config.sourcePath, path) {
				return fmt.Errorf("configuration path and %s must differ", name)
			}
		}
	}
	return nil
}

func knownSamePath(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo) {
		return true
	}
	leftCanonical, leftKnown := canonicalKnownPath(left)
	rightCanonical, rightKnown := canonicalKnownPath(right)
	return leftKnown && rightKnown && leftCanonical == rightCanonical
}

func canonicalKnownPath(path string) (string, bool) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	absolute = filepath.Clean(absolute)
	candidate := absolute
	var missingComponents []string
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			for index := len(missingComponents) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missingComponents[index])
			}
			return filepath.Clean(resolved), true
		}

		parent := filepath.Dir(candidate)
		if parent == candidate {
			return absolute, true
		}
		missingComponents = append(missingComponents, filepath.Base(candidate))
		candidate = parent
	}
}
