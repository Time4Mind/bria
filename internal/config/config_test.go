package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"bria/internal/config"
	"bria/internal/domain"
)

const validConfigJSONTemplate = `{
  "owner_user_id": 123456789,
  "private_chat_id": 123456789,
  "bot_username": "@my_bria_bot",
  "state_path": "/var/lib/bria/sessions.json",
  "telegram_token": {"env_var": "BRIA_TELEGRAM_TOKEN"},
  "callback_key": {"secret_file": "/var/lib/bria/callback.key"},
  "providers": {
    "codex": {
      "enabled": true,
      "command": {"exec": VALID_EXECUTABLE, "argv": []}
    },
    "claude": {"enabled": false}
  }
}`

var (
	validExecutablePath = mustCurrentExecutable()
	validConfigJSON     = strings.Replace(
		validConfigJSONTemplate,
		"VALID_EXECUTABLE",
		strconv.Quote(validExecutablePath),
		1,
	)
)

func mustCurrentExecutable() string {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return path
}

func TestDecodeAcceptsExplicitConfiguration(t *testing.T) {
	t.Parallel()

	got, err := config.Decode(strings.NewReader(validConfigJSON))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.OwnerUserID != 123456789 {
		t.Errorf("OwnerUserID = %d, want 123456789", got.OwnerUserID)
	}
	if got.PrivateChatID != 123456789 {
		t.Errorf("PrivateChatID = %d, want 123456789", got.PrivateChatID)
	}
	if got.BotUsername != "my_bria_bot" {
		t.Errorf("BotUsername = %q, want normalized username without @", got.BotUsername)
	}
	if got.StatePath != "/var/lib/bria/sessions.json" {
		t.Errorf("StatePath = %q, want configured absolute path", got.StatePath)
	}
	if got.TelegramToken.EnvVar != "BRIA_TELEGRAM_TOKEN" || got.TelegramToken.SecretFile != "" {
		t.Errorf("TelegramToken = %#v, want env reference only", got.TelegramToken)
	}
	if len(got.Providers) != 2 || !got.Providers["codex"].Enabled || got.Providers["claude"].Enabled {
		t.Errorf("Providers = %#v, want explicit enabled codex and disabled claude", got.Providers)
	}
}

func TestDecodeNormalizesAndValidatesBotUsername(t *testing.T) {
	t.Parallel()

	for _, configured := range []string{"my_bria_bot", "@my_bria_bot", "ExampleBot", "@ExampleBot"} {
		configured := configured
		t.Run(configured, func(t *testing.T) {
			document := strings.Replace(validConfigJSON, `"@my_bria_bot"`, strconv.Quote(configured), 1)
			got, err := config.Decode(strings.NewReader(document))
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if strings.HasPrefix(got.BotUsername, "@") {
				t.Fatalf("BotUsername = %q, want normalized value", got.BotUsername)
			}
		})
	}

	invalid := []string{
		"bot",
		"abcd_bot_name_that_is_more_than_32_chars_bot",
		"1bria_bot",
		"_bria_bot",
		"bria-bot",
		"bria bot",
		"bria",
		"@@my_bria_bot",
		"бриа_bot",
	}
	for _, configured := range invalid {
		configured := configured
		t.Run("invalid_"+configured, func(t *testing.T) {
			document := strings.Replace(validConfigJSON, `"@my_bria_bot"`, strconv.Quote(configured), 1)
			if _, err := config.Decode(strings.NewReader(document)); err == nil {
				t.Fatalf("Decode() error = nil for invalid bot username %q", configured)
			}
		})
	}
}

func TestEnabledCommandReturnsOnlyEnabledProviderAndDefensiveArgv(t *testing.T) {
	t.Parallel()

	document := strings.Replace(validConfigJSON, `"argv": []`, `"argv": ["serve", "--stdio"]`, 1)
	configuration := decodeConfig(t, document)

	command, ok := configuration.EnabledCommand(domain.ProviderCodex)
	if !ok {
		t.Fatal("EnabledCommand(codex) ok = false, want true")
	}
	if command.Exec != validExecutablePath ||
		len(command.Argv) != 2 || command.Argv[0] != "serve" || command.Argv[1] != "--stdio" {
		t.Fatalf("EnabledCommand(codex) = %#v, want configured exec and argv", command)
	}
	command.Argv[0] = "mutated"
	again, ok := configuration.EnabledCommand(domain.ProviderCodex)
	if !ok || again.Argv[0] != "serve" {
		t.Fatalf("second EnabledCommand(codex) = (%#v, %v), want independent argv", again, ok)
	}
	if _, ok := configuration.EnabledCommand(domain.ProviderClaude); ok {
		t.Fatal("EnabledCommand(claude) ok = true, want disabled provider hidden")
	}
	if _, ok := configuration.EnabledCommand(domain.Provider("other")); ok {
		t.Fatal("EnabledCommand(other) ok = true, want unknown provider hidden")
	}

	configuration.Providers["codex"].Command.Exec = shellExecutableForTest(t)
	if _, ok := configuration.EnabledCommand(domain.ProviderCodex); ok {
		t.Fatal("EnabledCommand(codex) returned command after configuration became invalid")
	}
}

func TestProviderCapabilitiesExposeEnabledAndConfiguredState(t *testing.T) {
	t.Parallel()

	document := strings.Replace(validConfigJSON, `"enabled": true`, `"enabled": false`, 1)
	configuration, err := config.Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode(all disabled) error = %v, want Bria to remain operable for authorization", err)
	}
	capabilities := configuration.ProviderCapabilities()
	if len(capabilities) != 2 || capabilities[0].Provider != domain.ProviderCodex ||
		capabilities[0].Enabled || !capabilities[0].Configured ||
		capabilities[1].Provider != domain.ProviderClaude || capabilities[1].Enabled || capabilities[1].Configured {
		t.Fatalf("ProviderCapabilities() = %#v", capabilities)
	}
	if configuration.ProviderEnabled(domain.ProviderCodex) || configuration.ProviderEnabled(domain.ProviderClaude) {
		t.Fatal("ProviderEnabled() = true for disabled providers")
	}
}

func TestWithProviderEnabledPreservesCapabilityAndRejectsImpossibleEnable(t *testing.T) {
	t.Parallel()

	configuration := decodeConfig(t, validConfigJSON)
	disabled, err := configuration.WithProviderEnabled(domain.ProviderCodex, false)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.ProviderEnabled(domain.ProviderCodex) {
		t.Fatal("WithProviderEnabled(false) left Codex enabled")
	}
	capabilities := disabled.ProviderCapabilities()
	if len(capabilities) != 2 || !capabilities[0].Configured {
		t.Fatalf("disabled capabilities = %#v, want command retained", capabilities)
	}
	reenabled, err := disabled.WithProviderEnabled(domain.ProviderCodex, true)
	if err != nil || !reenabled.ProviderEnabled(domain.ProviderCodex) {
		t.Fatalf("WithProviderEnabled(true) = (%#v, %v)", reenabled, err)
	}
	if configuration.ProviderEnabled(domain.ProviderClaude) || !configuration.ProviderEnabled(domain.ProviderCodex) {
		t.Fatal("WithProviderEnabled mutated original configuration")
	}
	if _, err := configuration.WithProviderEnabled(domain.ProviderClaude, true); err == nil {
		t.Fatal("enabled unconfigured Claude without an executable command")
	}
	if _, err := configuration.WithProviderEnabled(domain.Provider("other"), true); err == nil {
		t.Fatal("enabled unsupported provider")
	}
}

func TestDecodeRejectsBriaOwnedModelAndReasoningOverrides(t *testing.T) {
	t.Parallel()

	tests := []string{
		`["--model", "gpt-5"]`,
		`["--model=opus"]`,
		`["--effort", "high"]`,
		`["--reasoning-effort=high"]`,
		`["-c", "model_reasoning_effort=high"]`,
		`["--config=model=gpt-5"]`,
	}
	for _, argv := range tests {
		argv := argv
		t.Run(argv, func(t *testing.T) {
			document := strings.Replace(validConfigJSON, `"argv": []`, `"argv": `+argv, 1)
			if _, err := config.Decode(strings.NewReader(document)); err == nil {
				t.Fatal("Decode() error = nil, want model/reasoning override rejected")
			}
		})
	}
}

func TestDecodeRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()

	duplicate := strings.Replace(
		validConfigJSON,
		`"owner_user_id": 123456789,`,
		`"owner_user_id": 123456789, "owner_user_id": 987654321,`,
		1,
	)
	if _, err := config.Decode(strings.NewReader(duplicate)); err == nil {
		t.Fatal("Decode() error = nil, want duplicate-key rejection")
	}
}

func TestDecodeRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	replace := func(old, next string) string {
		t.Helper()
		updated := strings.Replace(validConfigJSON, old, next, 1)
		if updated == validConfigJSON {
			t.Fatalf("test replacement %q was not found", old)
		}
		return updated
	}

	tests := []struct {
		name     string
		document string
		secret   string
	}{
		{
			name: "literal token field",
			document: replace(
				`"owner_user_id": 123456789,`,
				`"token": "literal-secret", "owner_user_id": 123456789,`,
			),
			secret: "literal-secret",
		},
		{name: "unknown top-level field", document: replace(`"owner_user_id": 123456789,`, `"extra": true, "owner_user_id": 123456789,`)},
		{name: "case-insensitive field alias", document: replace(`"owner_user_id"`, `"OWNER_USER_ID"`)},
		{name: "trailing document", document: validConfigJSON + `{}`},
		{name: "oversized document", document: validConfigJSON + strings.Repeat(" ", config.MaxDocumentBytes)},
		{name: "zero owner", document: replace(`"owner_user_id": 123456789`, `"owner_user_id": 0`)},
		{name: "negative chat", document: replace(`"private_chat_id": 123456789`, `"private_chat_id": -1`)},
		{name: "missing bot username", document: replace(`"bot_username": "@my_bria_bot",`, ``)},
		{name: "relative state path", document: replace(`/var/lib/bria/sessions.json`, `relative/sessions.json`)},
		{name: "missing token source", document: replace(`{"env_var": "BRIA_TELEGRAM_TOKEN"}`, `{}`)},
		{name: "both token sources", document: replace(`{"env_var": "BRIA_TELEGRAM_TOKEN"}`, `{"env_var": "BRIA_TELEGRAM_TOKEN", "secret_file": "/run/secrets/bria"}`)},
		{name: "missing callback key", document: replace(`"callback_key": {"secret_file": "/var/lib/bria/callback.key"},`, ``)},
		{name: "callback key environment source", document: replace(`{"secret_file": "/var/lib/bria/callback.key"}`, `{"env_var": "BRIA_CALLBACK_KEY"}`)},
		{name: "relative callback key", document: replace(`/var/lib/bria/callback.key`, `relative-callback-key`)},
		{name: "callback key and state collide", document: replace(`/var/lib/bria/callback.key`, `/var/lib/bria/sessions.json`)},
		{name: "relative token file", document: replace(`{"env_var": "BRIA_TELEGRAM_TOKEN"}`, `{"secret_file": "relative-token"}`)},
		{name: "token and state collide", document: replace(`{"env_var": "BRIA_TELEGRAM_TOKEN"}`, `{"secret_file": "/var/lib/bria/sessions.json"}`)},
		{name: "env starts with digit", document: replace(`BRIA_TELEGRAM_TOKEN`, `9BRIA_TOKEN`)},
		{name: "env contains unicode", document: replace(`BRIA_TELEGRAM_TOKEN`, `BRIA_ТОКЕН`)},
		{name: "missing provider", document: replace(`,
    "claude": {"enabled": false}`, ``)},
		{name: "unknown provider", document: replace(`"claude": {"enabled": false}`, `"other": {"enabled": false}`)},
		{name: "provider missing explicit enabled", document: replace(`"claude": {"enabled": false}`, `"claude": {}`)},
		{name: "enabled without command", document: replace(",\n      \"command\": {\"exec\": "+strconv.Quote(validExecutablePath)+`, "argv": []}`, ``)},
		{name: "relative executable", document: replace(strconv.Quote(validExecutablePath), strconv.Quote(`bria-codex-adapter`))},
		{name: "shell executable", document: replace(strconv.Quote(validExecutablePath), strconv.Quote(shellExecutableForTest(t)))},
		{name: "missing argv", document: replace(`, "argv": []`, ``)},
		{name: "NUL executable", document: replace(strconv.Quote(validExecutablePath), `"/usr/local/bin/bria\u0000adapter"`)},
		{name: "NUL argument", document: replace(`"argv": []`, `"argv": ["bad\u0000arg"]`)},
		{name: "shell field", document: replace(`"argv": []`, `"argv": [], "shell": true`)},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Decode(strings.NewReader(test.document))
			if err == nil {
				t.Fatal("Decode() error = nil, want rejection")
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("Decode() error leaked literal token: %v", err)
			}
		})
	}
}

func TestDecodeRejectsInvalidUTF8WithoutReplacement(t *testing.T) {
	t.Parallel()

	document := bytes.Replace(
		[]byte(validConfigJSON),
		[]byte(`"argv": []`),
		[]byte{'"', 'a', 'r', 'g', 'v', '"', ':', ' ', '[', '"', 0xff, '"', ']'},
		1,
	)
	if _, err := config.Decode(bytes.NewReader(document)); err == nil {
		t.Fatal("Decode() error = nil, want invalid UTF-8 rejection")
	}
}

func TestDecodeRejectsUnsafeEnabledExecutableTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "missing", path: filepath.Join(t.TempDir(), "missing")},
		{name: "directory", path: t.TempDir()},
	}

	if runtime.GOOS != "windows" {
		nonExecutable := filepath.Join(t.TempDir(), "adapter")
		if err := os.WriteFile(nonExecutable, []byte("not executable"), 0o600); err != nil {
			t.Fatalf("write non-executable target: %v", err)
		}
		tests = append(tests, struct {
			name string
			path string
		}{name: "not executable", path: nonExecutable})
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := strings.Replace(
				validConfigJSON,
				strconv.Quote(validExecutablePath),
				strconv.Quote(test.path),
				1,
			)
			if _, err := config.Decode(strings.NewReader(document)); err == nil {
				t.Fatalf("Decode() error = nil for enabled executable %q", test.path)
			}
		})
	}
}

func TestDecodeRejectsEnabledExecutableSymlinkResolvingToShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}

	directory := t.TempDir()
	shellTarget := filepath.Join(directory, "sh")
	if err := os.WriteFile(shellTarget, []byte("fake shell"), 0o700); err != nil {
		t.Fatalf("write shell target: %v", err)
	}
	alias := filepath.Join(directory, "bria-adapter")
	if err := os.Symlink(shellTarget, alias); err != nil {
		t.Fatalf("create shell alias: %v", err)
	}
	document := strings.Replace(
		validConfigJSON,
		strconv.Quote(validExecutablePath),
		strconv.Quote(alias),
		1,
	)
	if _, err := config.Decode(strings.NewReader(document)); err == nil {
		t.Fatal("Decode() error = nil, want resolved shell target rejection")
	}
}

func shellExecutableForTest(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"/bin/sh", "/usr/bin/sh"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	if runtime.GOOS == "windows" {
		return `C:\\Windows\\System32\\cmd.exe`
	}
	t.Fatal("test platform has no known shell executable")
	return ""
}
