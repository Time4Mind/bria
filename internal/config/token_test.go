package config_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"bria/internal/config"
)

func TestReadTokenFromNamedEnvironmentVariable(t *testing.T) {
	const secret = "telegram-secret-from-environment"
	t.Setenv("BRIA_TELEGRAM_TOKEN", secret)

	configuration := decodeConfig(t, validConfigJSON)
	got, err := configuration.ReadToken()
	if err != nil {
		t.Fatalf("ReadToken() error = %v", err)
	}
	if string(got) != secret {
		t.Errorf("ReadToken() = %q, want configured environment value", got)
	}
}

func TestReadTokenRejectsBlankEnvironmentWithoutLeak(t *testing.T) {
	for _, secret := range []string{"", " \t\n"} {
		secret := secret
		t.Run(strconv.Quote(secret), func(t *testing.T) {
			t.Setenv("BRIA_TELEGRAM_TOKEN", secret)
			configuration := decodeConfig(t, validConfigJSON)

			_, err := configuration.ReadToken()
			if err == nil {
				t.Fatal("ReadToken() error = nil, want blank-token rejection")
			}
			if secret != "" && strings.Contains(err.Error(), secret) {
				t.Fatalf("ReadToken() error leaked token value: %v", err)
			}
		})
	}
}

func TestReadTokenRejectsOversizedEnvironmentValue(t *testing.T) {
	t.Setenv("BRIA_TELEGRAM_TOKEN", strings.Repeat("x", (8<<10)+1))
	configuration := decodeConfig(t, validConfigJSON)

	if _, err := configuration.ReadToken(); err == nil {
		t.Fatal("ReadToken() error = nil, want oversized environment token rejection")
	}
}

func TestReadTokenRejectsNonVisibleASCIIFromEverySource(t *testing.T) {
	invalid := []string{
		"token with space",
		"token\twith-tab",
		"token\x00with-nul",
		"token\x7fwith-del",
		"token-with-é",
	}
	for _, secret := range invalid {
		secret := secret
		t.Run(strconv.Quote(secret), func(t *testing.T) {
			if !strings.ContainsRune(secret, '\x00') {
				t.Run("environment", func(t *testing.T) {
					t.Setenv("BRIA_TELEGRAM_TOKEN", secret)
					configuration := decodeConfig(t, validConfigJSON)
					if _, err := configuration.ReadToken(); err == nil {
						t.Fatal("ReadToken() error = nil, want non-visible-ASCII rejection")
					}
				})
			}
			t.Run("file", func(t *testing.T) {
				path := writeSecretFixture(t, secret)
				configuration := decodeConfig(t, configWithSecretFile(path))
				if _, err := configuration.ReadToken(); err == nil {
					t.Fatal("ReadToken() error = nil, want non-visible-ASCII rejection")
				}
			})
		})
	}
}

func TestReadTokenAllowsTelegramAlphabetPunctuation(t *testing.T) {
	const secret = "123456789:ABC_def-GHI"
	t.Setenv("BRIA_TELEGRAM_TOKEN", secret)
	configuration := decodeConfig(t, validConfigJSON)

	got, err := configuration.ReadToken()
	if err != nil {
		t.Fatalf("ReadToken() error = %v", err)
	}
	if string(got) != secret {
		t.Fatalf("ReadToken() = %q, want %q", got, secret)
	}
}

func TestReadTokenFromExactModeRegularFile(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "no newline", content: "file-secret"},
		{name: "one LF", content: "file-secret\n"},
		{name: "one CRLF", content: "file-secret\r\n"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "telegram-token")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatalf("write token fixture: %v", err)
			}
			configuration := decodeConfig(t, configWithSecretFile(path))

			got, err := configuration.ReadToken()
			if err != nil {
				t.Fatalf("ReadToken() error = %v", err)
			}
			if string(got) != "file-secret" {
				t.Errorf("ReadToken() = %q, want one terminal newline removed", got)
			}
		})
	}
}

func TestReadTokenRejectsUnsafeSecretFileWithoutLeak(t *testing.T) {
	const secret = "never-print-this-secret"
	tests := []struct {
		name  string
		setup func(*testing.T) string
	}{
		{
			name: "symlink",
			setup: func(t *testing.T) string {
				directory := t.TempDir()
				target := filepath.Join(directory, "target")
				if err := os.WriteFile(target, []byte(secret), 0o600); err != nil {
					t.Fatalf("write symlink target: %v", err)
				}
				path := filepath.Join(directory, "telegram-token")
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create token symlink: %v", err)
				}
				return path
			},
		},
		{
			name: "wrong mode",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "telegram-token")
				if err := os.WriteFile(path, []byte(secret), 0o644); err != nil {
					t.Fatalf("write token fixture: %v", err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatalf("chmod token fixture: %v", err)
				}
				return path
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name: "blank",
			setup: func(t *testing.T) string {
				return writeSecretFixture(t, " \t\n")
			},
		},
		{
			name: "two newlines",
			setup: func(t *testing.T) string {
				return writeSecretFixture(t, secret+"\n\n")
			},
		},
		{
			name: "surrounding space",
			setup: func(t *testing.T) string {
				return writeSecretFixture(t, " "+secret)
			},
		},
		{
			name: "oversized",
			setup: func(t *testing.T) string {
				return writeSecretFixture(t, strings.Repeat("x", 2<<20))
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			path := test.setup(t)
			configuration := decodeConfig(t, configWithSecretFile(path))

			_, err := configuration.ReadToken()
			if err == nil {
				t.Fatal("ReadToken() error = nil, want unsafe-file rejection")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("ReadToken() error leaked token value: %v", err)
			}
		})
	}
}

func decodeConfig(t *testing.T, document string) config.Config {
	t.Helper()
	configuration, err := config.Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return configuration
}

func configWithSecretFile(path string) string {
	return strings.Replace(
		validConfigJSON,
		`{"env_var": "BRIA_TELEGRAM_TOKEN"}`,
		fmt.Sprintf(`{"secret_file": %s}`, strconv.Quote(path)),
		1,
	)
}

func configWithCallbackKey(path string) string {
	return strings.Replace(
		validConfigJSON,
		`{"secret_file": "/var/lib/bria/callback.key"}`,
		fmt.Sprintf(`{"secret_file": %s}`, strconv.Quote(path)),
		1,
	)
}

func writeSecretFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telegram-token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write token fixture: %v", err)
	}
	return path
}

func TestReadTokenReturnsIndependentBytes(t *testing.T) {
	const secret = "independent-secret"
	t.Setenv("BRIA_TELEGRAM_TOKEN", secret)
	configuration := decodeConfig(t, validConfigJSON)

	first, err := configuration.ReadToken()
	if err != nil {
		t.Fatalf("first ReadToken() error = %v", err)
	}
	second, err := configuration.ReadToken()
	if err != nil {
		t.Fatalf("second ReadToken() error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("token reads differ: %q vs %q", first, second)
	}
	first[0] = 'X'
	if bytes.Equal(first, second) {
		t.Fatal("ReadToken() returned aliased mutable bytes")
	}
}

func TestReadTokenRejectsMissingEnvironmentVariable(t *testing.T) {
	const name = "BRIA_DEFINITELY_MISSING_TOKEN"
	if _, exists := os.LookupEnv(name); exists {
		t.Fatalf("test environment unexpectedly contains %s", name)
	}
	configuration := decodeConfig(t, strings.Replace(validConfigJSON, "BRIA_TELEGRAM_TOKEN", name, 1))
	_, err := configuration.ReadToken()
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadToken() error = %v, want safe missing-env error", err)
	}
}

func TestReadCallbackKeyFromSecureFileReturnsIndependentRawBytes(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, ending := range []string{"", "\n", "\r\n"} {
		ending := ending
		t.Run(strconv.Quote(ending), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "callback-key")
			content := append(append([]byte(nil), key...), []byte(ending)...)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatalf("write callback key fixture: %v", err)
			}
			configuration := decodeConfig(t, configWithCallbackKey(path))

			first, err := configuration.ReadCallbackKey()
			if err != nil {
				t.Fatalf("ReadCallbackKey() error = %v", err)
			}
			second, err := configuration.ReadCallbackKey()
			if err != nil {
				t.Fatalf("second ReadCallbackKey() error = %v", err)
			}
			if !bytes.Equal(first, key) || !bytes.Equal(second, key) {
				t.Fatalf("ReadCallbackKey() values differ from raw key")
			}
			first[0] ^= 0xff
			if bytes.Equal(first, second) {
				t.Fatal("ReadCallbackKey() returned aliased mutable bytes")
			}
		})
	}
}

func TestReadCallbackKeyAcceptsArbitraryRawBytesWithinBounds(t *testing.T) {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index)
	}
	path := filepath.Join(t.TempDir(), "callback-key")
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("write callback key fixture: %v", err)
	}
	configuration := decodeConfig(t, configWithCallbackKey(path))
	got, err := configuration.ReadCallbackKey()
	if err != nil {
		t.Fatalf("ReadCallbackKey() error = %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("ReadCallbackKey() changed raw key bytes")
	}
}

func TestReadCallbackKeyRejectsUnsafeFileAndInvalidLengthWithoutLeak(t *testing.T) {
	const sentinel = "never-log-this-callback-key"
	tests := []struct {
		name  string
		setup func(*testing.T) string
	}{
		{name: "short", setup: func(t *testing.T) string { return writeCallbackFixture(t, []byte(sentinel)) }},
		{name: "long", setup: func(t *testing.T) string { return writeCallbackFixture(t, bytes.Repeat([]byte("x"), 65)) }},
		{name: "long before LF", setup: func(t *testing.T) string { return writeCallbackFixture(t, append(bytes.Repeat([]byte("x"), 65), '\n')) }},
		{name: "two LF", setup: func(t *testing.T) string {
			return writeCallbackFixture(t, append(bytes.Repeat([]byte("x"), 32), '\n', '\n'))
		}},
		{name: "directory", setup: func(t *testing.T) string { return t.TempDir() }},
		{name: "wrong mode", setup: func(t *testing.T) string {
			path := writeCallbackFixture(t, bytes.Repeat([]byte("x"), 32))
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod callback key: %v", err)
			}
			return path
		}},
		{name: "symlink", setup: func(t *testing.T) string {
			directory := t.TempDir()
			target := filepath.Join(directory, "target")
			if err := os.WriteFile(target, bytes.Repeat([]byte("x"), 32), 0o600); err != nil {
				t.Fatalf("write callback target: %v", err)
			}
			alias := filepath.Join(directory, "alias")
			if err := os.Symlink(target, alias); err != nil {
				t.Fatalf("create callback symlink: %v", err)
			}
			return alias
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			path := test.setup(t)
			configuration := decodeConfig(t, configWithCallbackKey(path))
			_, err := configuration.ReadCallbackKey()
			if err == nil {
				t.Fatal("ReadCallbackKey() error = nil, want rejection")
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("ReadCallbackKey() error leaked key bytes: %v", err)
			}
		})
	}
}

func writeCallbackFixture(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "callback-key")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write callback key fixture: %v", err)
	}
	return path
}
