package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxTokenBytes = 8 << 10

const (
	minCallbackKeyBytes = 32
	maxCallbackKeyBytes = 64
)

// ReadToken resolves the configured token reference without retaining the
// secret in Config. Returned bytes are newly allocated for this call.
func (config Config) ReadToken() ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.TelegramToken.EnvVar != "" {
		value, ok := os.LookupEnv(config.TelegramToken.EnvVar)
		if !ok {
			return nil, errors.New("Telegram token environment variable is not set")
		}
		if len(value) > maxTokenBytes {
			return nil, errors.New("Telegram token environment variable is too large")
		}
		return normalizeToken([]byte(value), false)
	}
	return readSecretFile(config.TelegramToken.SecretFile)
}

// ReadCallbackKey loads the callback-signing key without retaining it in
// Config. Returned raw key bytes are newly allocated for this call.
func (config Config) ReadCallbackKey() ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return readCallbackKeyFile(config.CallbackKey.SecretFile)
}

func readCallbackKeyFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect callback key secret file: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("callback key secret file must not be a symlink")
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("callback key secret file must be regular")
	}
	if before.Mode().Perm() != 0o600 {
		return nil, errors.New("callback key secret file must have mode 0600")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open callback key secret file: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("verify callback key secret file: %w", err)
	}
	if !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 || !os.SameFile(before, after) {
		return nil, errors.New("callback key secret file changed during secure open")
	}

	limited := io.LimitReader(file, maxCallbackKeyBytes+3)
	value, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.New("read callback key secret file")
	}
	if len(value) > maxCallbackKeyBytes+2 {
		return nil, errors.New("callback key secret file is too large")
	}
	return normalizeCallbackKey(value)
}

func normalizeCallbackKey(value []byte) ([]byte, error) {
	clean := append([]byte(nil), value...)
	switch {
	case bytes.HasSuffix(clean, []byte("\r\n")):
		clean = clean[:len(clean)-2]
	case bytes.HasSuffix(clean, []byte("\n")):
		clean = clean[:len(clean)-1]
	}
	if bytes.HasSuffix(clean, []byte("\n")) {
		return nil, errors.New("callback key secret file has multiple terminal newlines")
	}
	if len(clean) < minCallbackKeyBytes || len(clean) > maxCallbackKeyBytes {
		return nil, fmt.Errorf(
			"callback key must contain between %d and %d raw bytes",
			minCallbackKeyBytes,
			maxCallbackKeyBytes,
		)
	}
	return append([]byte(nil), clean...), nil
}

func readSecretFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect Telegram token secret file: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Telegram token secret file must not be a symlink")
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("Telegram token secret file must be regular")
	}
	if before.Mode().Perm() != 0o600 {
		return nil, errors.New("Telegram token secret file must have mode 0600")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Telegram token secret file: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("verify Telegram token secret file: %w", err)
	}
	if !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 || !os.SameFile(before, after) {
		return nil, errors.New("Telegram token secret file changed during secure open")
	}

	limited := io.LimitReader(file, maxTokenBytes+1)
	value, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read Telegram token secret file: %w", err)
	}
	if len(value) > maxTokenBytes {
		return nil, errors.New("Telegram token secret file is too large")
	}
	return normalizeToken(value, true)
}

func normalizeToken(value []byte, allowTerminalNewline bool) ([]byte, error) {
	clean := append([]byte(nil), value...)
	if allowTerminalNewline {
		switch {
		case bytes.HasSuffix(clean, []byte("\r\n")):
			clean = clean[:len(clean)-2]
		case bytes.HasSuffix(clean, []byte("\n")):
			clean = clean[:len(clean)-1]
		}
	}
	if len(clean) == 0 {
		return nil, errors.New("Telegram token is blank")
	}
	for _, character := range clean {
		if character < 0x21 || character > 0x7e {
			return nil, errors.New("Telegram token must contain only visible ASCII characters")
		}
	}
	return append([]byte(nil), clean...), nil
}
