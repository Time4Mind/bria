// Package config loads the explicit local Bria configuration. Configuration
// contains only references to secrets; resolved secret values are never stored
// in Config.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
)

const MaxDocumentBytes = 1 << 20

type Config struct {
	Version       int                       `json:"version,omitempty"`
	Role          Role                      `json:"role,omitempty"`
	Computer      *ComputerConfig           `json:"computer,omitempty"`
	OwnerUserID   int64                     `json:"owner_user_id"`
	PrivateChatID int64                     `json:"private_chat_id"`
	BotUsername   string                    `json:"bot_username"`
	StatePath     string                    `json:"state_path"`
	TelegramToken TelegramTokenRef          `json:"telegram_token"`
	CallbackKey   CallbackKeyRef            `json:"callback_key"`
	Network       *NetworkConfig            `json:"network,omitempty"`
	Paths         *RuntimePaths             `json:"paths,omitempty"`
	Providers     map[string]ProviderConfig `json:"providers"`
	Update        *UpdateConfig             `json:"update,omitempty"`
	Backup        *BackupConfig             `json:"backup,omitempty"`
	Parakeet      *ParakeetConfig           `json:"parakeet,omitempty"`
	MediaLimits   *MediaLimits              `json:"media_limits,omitempty"`
	Runtime       *RuntimeFeatures          `json:"runtime,omitempty"`

	sourcePath string
}

type TelegramTokenRef struct {
	EnvVar     string `json:"env_var,omitempty"`
	SecretFile string `json:"secret_file,omitempty"`
}

type CallbackKeyRef struct {
	SecretFile string `json:"secret_file"`
}

type ProviderConfig struct {
	Enabled bool             `json:"enabled"`
	Command *ProviderCommand `json:"command,omitempty"`
}

// ProviderCommand is executed directly. Argv contains arguments after argv[0]
// and is never interpreted by a shell.
type ProviderCommand struct {
	Exec string   `json:"exec"`
	Argv []string `json:"argv"`
}

// ProviderCapability is the local availability state exposed to composition.
// Configured means a command is present; Enabled controls whether it may be
// selected without deleting provider-owned authorization data.
type ProviderCapability struct {
	Provider   domain.Provider
	Enabled    bool
	Configured bool
}

func (config Config) ProviderCapabilities() []ProviderCapability {
	if config.Validate() != nil || config.validateSourcePathCollisions() != nil {
		return nil
	}
	capabilities := make([]ProviderCapability, 0, 2)
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		configured := config.Providers[string(provider)]
		capabilities = append(capabilities, ProviderCapability{
			Provider: provider, Enabled: configured.Enabled, Configured: configured.Command != nil,
		})
	}
	return capabilities
}

func (config Config) ProviderEnabled(provider domain.Provider) bool {
	configured, ok := config.Providers[string(provider)]
	return ok && configured.Enabled && config.Validate() == nil && config.validateSourcePathCollisions() == nil
}

// P4Runtime returns a defensive copy of the enabled P4 composition inputs.
// A false result means P4 remains disabled and must not be constructed.
func (config Config) P4Runtime() (P4RuntimeConfig, bool) {
	if config.Validate() != nil || config.validateSourcePathCollisions() != nil || config.Runtime == nil || config.Runtime.P4 == nil {
		return P4RuntimeConfig{}, false
	}
	return *cloneP4Runtime(config.Runtime.P4), true
}

// DiscoveryRuntime returns a defensive copy of the enabled discovery inputs.
func (config Config) DiscoveryRuntime() (DiscoveryRuntimeConfig, bool) {
	if config.Validate() != nil || config.validateSourcePathCollisions() != nil || config.Runtime == nil || config.Runtime.Discovery == nil {
		return DiscoveryRuntimeConfig{}, false
	}
	return *cloneValue(config.Runtime.Discovery), true
}

// BackupRuntime returns a defensive copy of the enabled backup runtime inputs.
func (config Config) BackupRuntime() (BackupRuntimeConfig, bool) {
	if config.Validate() != nil || config.validateSourcePathCollisions() != nil || config.Runtime == nil || config.Runtime.Backup == nil {
		return BackupRuntimeConfig{}, false
	}
	return *cloneValue(config.Runtime.Backup), true
}

// UpdateRuntime returns a defensive copy of the enabled update runtime inputs.
func (config Config) UpdateRuntime() (UpdateRuntimeConfig, bool) {
	if config.Validate() != nil || config.validateSourcePathCollisions() != nil || config.Runtime == nil || config.Runtime.Update == nil {
		return UpdateRuntimeConfig{}, false
	}
	return *cloneValue(config.Runtime.Update), true
}

// WithProviderEnabled returns a validated copy with one local capability
// toggled. It retains the configured command and does not read, rewrite, or
// delete provider-owned authorization data.
func (config Config) WithProviderEnabled(provider domain.Provider, enabled bool) (Config, error) {
	name := string(provider)
	if name != string(domain.ProviderCodex) && name != string(domain.ProviderClaude) {
		return Config{}, errors.New("unsupported provider")
	}
	next := cloneConfig(config)
	if next.Providers == nil {
		next.Providers = make(map[string]ProviderConfig)
	}
	capability := next.Providers[name]
	capability.Enabled = enabled
	next.Providers[name] = capability
	if err := next.Validate(); err != nil {
		return Config{}, fmt.Errorf("update provider %q: %w", name, err)
	}
	if err := next.validateSourcePathCollisions(); err != nil {
		return Config{}, fmt.Errorf("update provider %q: %w", name, err)
	}
	return next, nil
}

// EnabledCommand returns a provider-neutral defensive copy of the direct
// command for an enabled configured provider.
func (config Config) EnabledCommand(provider domain.Provider) (ProviderCommand, bool) {
	if config.Validate() != nil || config.validateSourcePathCollisions() != nil {
		return ProviderCommand{}, false
	}
	configured, ok := config.Providers[string(provider)]
	if !ok || !configured.Enabled || configured.Command == nil {
		return ProviderCommand{}, false
	}
	command := *configured.Command
	command.Argv = make([]string, len(configured.Command.Argv))
	copy(command.Argv, configured.Command.Argv)
	return command, true
}

// Decode reads one complete strict JSON configuration. It rejects oversized
// input, duplicate or unknown keys, and trailing JSON values.
func Decode(reader io.Reader) (Config, error) {
	limited := io.LimitReader(reader, MaxDocumentBytes+1)
	document, err := io.ReadAll(limited)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	if len(document) > MaxDocumentBytes {
		return Config{}, fmt.Errorf("configuration exceeds %d bytes", MaxDocumentBytes)
	}
	if !utf8.Valid(document) {
		return Config{}, errors.New("configuration must be valid UTF-8")
	}
	if err := rejectDuplicateKeys(document); err != nil {
		return Config{}, fmt.Errorf("validate configuration JSON: %w", err)
	}
	if err := validateExactJSONKeys(document); err != nil {
		return Config{}, fmt.Errorf("validate configuration fields: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("decode configuration: trailing JSON value")
		}
		return Config{}, fmt.Errorf("decode configuration trailing data: %w", err)
	}
	config.BotUsername = strings.TrimPrefix(config.BotUsername, "@")
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if config.Version != 0 {
		return config.validateVersioned()
	}
	return config.validateLegacy()
}

func (config Config) validateLegacy() error {
	if config.Role != "" || config.Computer != nil || config.Network != nil || config.Paths != nil ||
		config.Update != nil || config.Backup != nil || config.Parakeet != nil || config.MediaLimits != nil || config.Runtime != nil {
		return errors.New("legacy configuration cannot contain versioned fields; use explicit migration")
	}
	if config.OwnerUserID <= 0 {
		return errors.New("owner_user_id must be positive")
	}
	if config.PrivateChatID <= 0 {
		return errors.New("private_chat_id must be positive")
	}
	if err := validateBotUsername(config.BotUsername); err != nil {
		return err
	}
	if strings.TrimSpace(config.StatePath) == "" || !filepath.IsAbs(config.StatePath) {
		return errors.New("state_path must be absolute")
	}

	hasEnv := config.TelegramToken.EnvVar != ""
	hasFile := config.TelegramToken.SecretFile != ""
	if hasEnv == hasFile {
		return errors.New("telegram_token requires exactly one of env_var or secret_file")
	}
	if hasEnv && !validEnvironmentName(config.TelegramToken.EnvVar) {
		return errors.New("telegram_token env_var has invalid syntax")
	}
	if hasFile {
		if !filepath.IsAbs(config.TelegramToken.SecretFile) {
			return errors.New("telegram_token secret_file must be absolute")
		}
		if knownSamePath(config.StatePath, config.TelegramToken.SecretFile) {
			return errors.New("state_path and telegram_token secret_file must differ")
		}
	}
	if config.CallbackKey.SecretFile == "" || !filepath.IsAbs(config.CallbackKey.SecretFile) {
		return errors.New("callback_key secret_file must be an absolute path")
	}
	if knownSamePath(config.StatePath, config.CallbackKey.SecretFile) {
		return errors.New("state_path and callback_key secret_file must differ")
	}
	if hasFile && knownSamePath(config.TelegramToken.SecretFile, config.CallbackKey.SecretFile) {
		return errors.New("telegram_token and callback_key secret files must differ")
	}

	if len(config.Providers) != 2 {
		return errors.New("providers must explicitly contain only codex and claude")
	}
	for _, name := range []string{"codex", "claude"} {
		provider, ok := config.Providers[name]
		if !ok {
			return fmt.Errorf("providers must explicitly contain %q", name)
		}
		if provider.Enabled {
			if provider.Command == nil {
				return fmt.Errorf("enabled provider %q requires command", name)
			}
		}
		if provider.Command != nil {
			if err := validateProviderCommand(*provider.Command, provider.Enabled); err != nil {
				return fmt.Errorf("provider %q command: %w", name, err)
			}
		}
	}
	for name := range config.Providers {
		if name != "codex" && name != "claude" {
			return errors.New("unsupported provider")
		}
	}
	return nil
}

func validateBotUsername(username string) error {
	if strings.HasPrefix(username, "@") {
		return errors.New("bot_username must be normalized without @")
	}
	if len(username) < 5 || len(username) > 32 {
		return errors.New("bot_username must contain between 5 and 32 ASCII characters")
	}
	if !asciiLetter(username[0]) {
		return errors.New("bot_username must start with an ASCII letter")
	}
	for index := 1; index < len(username); index++ {
		character := username[index]
		if !asciiLetter(character) &&
			(character < '0' || character > '9') &&
			character != '_' {
			return errors.New("bot_username contains unsupported characters")
		}
	}
	if !strings.EqualFold(username[len(username)-3:], "bot") {
		return errors.New("bot_username must end with bot")
	}
	return nil
}

func asciiLetter(character byte) bool {
	return (character >= 'A' && character <= 'Z') ||
		(character >= 'a' && character <= 'z')
}

func validateProviderCommand(command ProviderCommand, requireRunnable bool) error {
	if strings.TrimSpace(command.Exec) == "" || !filepath.IsAbs(command.Exec) {
		return errors.New("exec must be absolute")
	}
	if strings.ContainsRune(command.Exec, '\x00') {
		return errors.New("exec contains NUL")
	}
	if isShellExecutable(command.Exec) {
		return errors.New("exec must not be a shell or command-discovery launcher")
	}
	if command.Argv == nil {
		return errors.New("argv must be explicitly specified")
	}
	for _, argument := range command.Argv {
		if strings.ContainsRune(argument, '\x00') {
			return errors.New("argv contains NUL")
		}
	}
	if err := rejectProviderPreferenceOverrides(command.Argv); err != nil {
		return err
	}
	if !requireRunnable {
		return nil
	}

	resolved, err := filepath.EvalSymlinks(command.Exec)
	if err != nil {
		return errors.New("exec target must exist")
	}
	if isShellExecutable(resolved) {
		return errors.New("resolved exec target must not be a shell or command-discovery launcher")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return errors.New("inspect resolved exec target")
	}
	if !info.Mode().IsRegular() {
		return errors.New("exec target must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return errors.New("exec target must be executable")
	}
	return nil
}

func rejectProviderPreferenceOverrides(argv []string) error {
	for _, argument := range argv {
		normalized := strings.ToLower(strings.TrimSpace(argument))
		switch normalized {
		case "-m", "--model", "--effort", "--reasoning-effort", "--reasoning_effort":
			return errors.New("model and reasoning overrides belong to provider settings")
		}
		for _, prefix := range []string{
			"--model=", "--effort=", "--reasoning-effort=", "--reasoning_effort=",
			"model=", "model_reasoning_effort=", "reasoning_effort=",
			"--config=model=", "--config=model_reasoning_effort=", "--config=reasoning_effort=",
		} {
			if strings.HasPrefix(normalized, prefix) {
				return errors.New("model and reasoning overrides belong to provider settings")
			}
		}
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if index == 0 {
			if character != '_' &&
				(character < 'A' || character > 'Z') &&
				(character < 'a' || character > 'z') {
				return false
			}
			continue
		}
		if character != '_' &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func isShellExecutable(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "csh", "tcsh",
		"cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe",
		"env", "env.exe":
		return true
	default:
		return false
	}
}

func validateExactJSONKeys(document []byte) error {
	top, err := decodeJSONObject(document)
	if err != nil {
		return err
	}
	if _, versioned := top["version"]; versioned {
		return validateVersionedJSON(top)
	}
	if err := rejectUnknownKeys(
		top,
		"owner_user_id",
		"private_chat_id",
		"bot_username",
		"state_path",
		"telegram_token",
		"callback_key",
		"providers",
	); err != nil {
		return err
	}
	for _, field := range []string{
		"owner_user_id", "private_chat_id", "bot_username", "state_path",
		"telegram_token", "callback_key", "providers",
	} {
		if _, ok := top[field]; !ok {
			return fmt.Errorf("missing field %q", field)
		}
	}
	if raw, ok := top["telegram_token"]; ok {
		token, err := decodeJSONObject(raw)
		if err != nil {
			return fmt.Errorf("telegram_token: %w", err)
		}
		if err := rejectUnknownKeys(token, "env_var", "secret_file"); err != nil {
			return fmt.Errorf("telegram_token: %w", err)
		}
	}
	if raw, ok := top["callback_key"]; ok {
		callbackKey, err := decodeJSONObject(raw)
		if err != nil {
			return fmt.Errorf("callback_key: %w", err)
		}
		if err := rejectUnknownKeys(callbackKey, "secret_file"); err != nil {
			return fmt.Errorf("callback_key: %w", err)
		}
		if _, ok := callbackKey["secret_file"]; !ok {
			return errors.New("callback_key: missing field \"secret_file\"")
		}
	}
	return validateProvidersJSON(top["providers"])
}

func decodeJSONObject(document []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(document, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("must be a JSON object")
	}
	return object, nil
}

func rejectUnknownKeys(object map[string]json.RawMessage, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowedSet[key]; !ok {
			return errors.New("unknown field")
		}
	}
	return nil
}

func rejectDuplicateKeys(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	if err := inspectJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func inspectJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return errors.New("duplicate object key")
			}
			keys[key] = struct{}{}
			if err := inspectJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := inspectJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	return nil
}
