package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const CurrentVersion = 1

type Role string

const (
	RoleCombined    Role = "combined"
	RoleCoordinator Role = "coordinator"
	RoleExecutor    Role = "executor"
)

type ComputerConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type NetworkConfig struct {
	CoordinatorAddress string `json:"coordinator_address"`
	ListenerAddress    string `json:"listener_address"`
	CertificateFile    string `json:"certificate_file"`
	PrivateKeyFile     string `json:"private_key_file"`
	TrustBundleFile    string `json:"trust_bundle_file"`
}

type RuntimePaths struct {
	PairingPath string `json:"pairing_path"`
	CatalogPath string `json:"catalog_path"`
	FencePath   string `json:"fence_path"`
	LedgerPath  string `json:"ledger_path"`
}

type UpdateConfig struct {
	SourceURL    string `json:"source_url"`
	TrustKeyFile string `json:"trust_key_file"`
}

// Schedule and Encryption deliberately remain nil until the product decisions
// explicitly left open in PRODUCT.md are made. The JSON fields are still
// required and must be null, preventing an implementation from choosing them.
type BackupConfig struct {
	Destination string  `json:"destination"`
	Schedule    *string `json:"schedule"`
	Encryption  *string `json:"encryption"`
}

// SecretFileRef names a secret without carrying its value in configuration.
type SecretFileRef struct {
	SecretFile string `json:"secret_file"`
}

// RuntimeFeatures is opt-in. A nil block keeps component-only runtimes
// unreachable; every present sub-block is complete and role-owned.
type RuntimeFeatures struct {
	P4        *P4RuntimeConfig        `json:"p4,omitempty"`
	Discovery *DiscoveryRuntimeConfig `json:"discovery,omitempty"`
	Backup    *BackupRuntimeConfig    `json:"backup,omitempty"`
	Update    *UpdateRuntimeConfig    `json:"update,omitempty"`
}

type P4RuntimeConfig struct {
	VoiceTempDirectory        string        `json:"voice_temp_directory"`
	PhotoCustodyDirectory     string        `json:"photo_custody_directory"`
	ArtifactManifestDirectory string        `json:"artifact_manifest_directory"`
	ArtifactRetryDirectory    string        `json:"artifact_retry_directory"`
	ArtifactAllowedRoots      []string      `json:"artifact_allowed_roots"`
	ArtifactRetryKey          SecretFileRef `json:"artifact_retry_key"`
	ArtifactRetryTTLSeconds   int64         `json:"artifact_retry_ttl_seconds"`
}

type DiscoveryRuntimeConfig struct {
	CodexRoot  string `json:"codex_root"`
	ClaudeRoot string `json:"claude_root"`
}

type BackupRuntimeConfig struct {
	WorkDirectory             string `json:"work_directory"`
	LatestPath                string `json:"latest_path"`
	RestoreCandidateDirectory string `json:"restore_candidate_directory"`
	LiveDirectory             string `json:"live_directory"`
	MarkerPath                string `json:"marker_path"`
	CodexHistoryRoot          string `json:"codex_history_root"`
	ClaudeHistoryRoot         string `json:"claude_history_root"`
	HarnessRoot               string `json:"harness_root"`
}

type UpdateRuntimeConfig struct {
	StageDirectory     string `json:"stage_directory"`
	StateDirectory     string `json:"state_directory"`
	InstallRoot        string `json:"install_root"`
	LockPath           string `json:"lock_path"`
	VerifierPath       string `json:"verifier_path"`
	FingerprintPath    string `json:"fingerprint_path"`
	InstallExecutable  string `json:"install_executable"`
	RollbackExecutable string `json:"rollback_executable"`
	ConfigFile         string `json:"config_file"`
	HealthProbePath    string `json:"health_probe_path"`
}

type ParakeetConfig struct {
	Executable string   `json:"executable"`
	ModelPath  string   `json:"model_path"`
	Argv       []string `json:"argv"`
}

type MediaLimits struct {
	DownloadBytes   int64 `json:"download_bytes"`
	UploadBytes     int64 `json:"upload_bytes"`
	VoiceBytes      int64 `json:"voice_bytes"`
	PhotoBytes      int64 `json:"photo_bytes"`
	TranscriptBytes int64 `json:"transcript_bytes"`
	DiagnosticBytes int64 `json:"diagnostic_bytes"`
}

// LegacyMigration is the explicit, caller-supplied completion of an old
// single-computer configuration. No computer identity, trust root, update
// source, backup destination, Parakeet reference or limit is guessed.
type LegacyMigration struct {
	Computer    ComputerConfig
	Network     NetworkConfig
	Paths       RuntimePaths
	Update      UpdateConfig
	Backup      BackupConfig
	Parakeet    ParakeetConfig
	MediaLimits MediaLimits
	Runtime     *RuntimeFeatures
}

func (config Config) IsLegacy() bool { return config.Version == 0 }

func (config Config) EffectiveRole() Role {
	if config.IsLegacy() {
		return RoleCombined
	}
	return config.Role
}

func (config Config) Coordinates() bool {
	role := config.EffectiveRole()
	return role == RoleCombined || role == RoleCoordinator
}

func (config Config) Executes() bool {
	role := config.EffectiveRole()
	return role == RoleCombined || role == RoleExecutor
}

func (config Config) MigrateLegacy(migration LegacyMigration) (Config, error) {
	if !config.IsLegacy() {
		return Config{}, errors.New("configuration is already versioned")
	}
	next := cloneConfig(config)
	next.Version = CurrentVersion
	next.Role = RoleCombined
	next.Computer = cloneValue(&migration.Computer)
	next.Network = cloneValue(&migration.Network)
	next.Paths = cloneValue(&migration.Paths)
	next.Update = cloneValue(&migration.Update)
	next.Backup = cloneBackup(&migration.Backup)
	next.Parakeet = cloneParakeet(&migration.Parakeet)
	next.MediaLimits = cloneValue(&migration.MediaLimits)
	next.Runtime = cloneRuntimeFeatures(migration.Runtime)
	if err := next.Validate(); err != nil {
		return Config{}, fmt.Errorf("migrate legacy configuration: %w", err)
	}
	if err := next.validateSourcePathCollisions(); err != nil {
		return Config{}, fmt.Errorf("migrate legacy configuration paths: %w", err)
	}
	return next, nil
}

func (config Config) validateVersioned() error {
	if config.Version != CurrentVersion {
		return fmt.Errorf("unsupported configuration version %d", config.Version)
	}
	if config.Role != RoleCombined && config.Role != RoleCoordinator && config.Role != RoleExecutor {
		return errors.New("role must be combined, coordinator, or executor")
	}
	if config.Computer == nil || !computerIDPattern.MatchString(config.Computer.ID) {
		return errors.New("computer.id has invalid syntax")
	}
	if config.Computer.Name == "" || strings.TrimSpace(config.Computer.Name) != config.Computer.Name ||
		!utf8.ValidString(config.Computer.Name) || len(config.Computer.Name) > 128 || hasControl(config.Computer.Name) {
		return errors.New("computer.name must be a stable non-empty display name")
	}
	if strings.TrimSpace(config.StatePath) == "" || !filepath.IsAbs(config.StatePath) || strings.ContainsRune(config.StatePath, 0) {
		return errors.New("state_path must be absolute")
	}

	if config.Coordinates() {
		if err := config.validateTelegram(); err != nil {
			return err
		}
	} else if config.OwnerUserID != 0 || config.PrivateChatID != 0 || config.BotUsername != "" ||
		config.TelegramToken != (TelegramTokenRef{}) || config.CallbackKey != (CallbackKeyRef{}) {
		return errors.New("executor role must not contain Telegram configuration")
	}
	if err := config.validateProviders(false); err != nil {
		return err
	}
	if err := config.validateNetwork(); err != nil {
		return err
	}
	if err := config.validateRuntimePaths(); err != nil {
		return err
	}
	if err := config.validateUpdate(); err != nil {
		return err
	}
	if err := config.validateBackup(); err != nil {
		return err
	}
	if err := config.validateParakeet(); err != nil {
		return err
	}
	if err := config.validateMediaLimits(); err != nil {
		return err
	}
	if err := config.validateRuntimeFeatures(); err != nil {
		return err
	}
	return validateNonOverlappingPaths(config.PathReferences())
}

func (config Config) validateTelegram() error {
	if config.OwnerUserID <= 0 {
		return errors.New("owner_user_id must be positive")
	}
	if config.PrivateChatID <= 0 {
		return errors.New("private_chat_id must be positive")
	}
	if err := validateBotUsername(config.BotUsername); err != nil {
		return err
	}
	hasEnv := config.TelegramToken.EnvVar != ""
	hasFile := config.TelegramToken.SecretFile != ""
	if hasEnv == hasFile {
		return errors.New("telegram_token requires exactly one reference")
	}
	if hasEnv && !validEnvironmentName(config.TelegramToken.EnvVar) {
		return errors.New("telegram_token env_var has invalid syntax")
	}
	if hasFile && !absolutePath(config.TelegramToken.SecretFile) {
		return errors.New("telegram_token secret_file must be absolute")
	}
	if !absolutePath(config.CallbackKey.SecretFile) {
		return errors.New("callback_key secret_file must be absolute")
	}
	return nil
}

func (config Config) validateProviders(legacy bool) error {
	if legacy && len(config.Providers) != 2 {
		return errors.New("providers must explicitly contain only codex and claude")
	}
	for name, provider := range config.Providers {
		if name != string(domainProviderCodex) && name != string(domainProviderClaude) {
			return errors.New("unsupported provider")
		}
		if provider.Enabled && provider.Command == nil {
			return fmt.Errorf("enabled provider %q requires command", name)
		}
		if provider.Command != nil {
			if err := validateProviderCommand(*provider.Command, provider.Enabled); err != nil {
				return fmt.Errorf("provider %q command: %w", name, err)
			}
		}
	}
	if legacy {
		for _, name := range []string{string(domainProviderCodex), string(domainProviderClaude)} {
			if _, ok := config.Providers[name]; !ok {
				return fmt.Errorf("providers must explicitly contain %q", name)
			}
		}
	}
	return nil
}

// Local aliases keep versioned validation independent from domain imports and
// still make the two supported provider names explicit.
const (
	domainProviderCodex  = "codex"
	domainProviderClaude = "claude"
)

func (config Config) validateNetwork() error {
	if config.Network == nil {
		return errors.New("network configuration is required")
	}
	network := config.Network
	if config.Role == RoleExecutor {
		if network.ListenerAddress != "" || !validHostPort(network.CoordinatorAddress) {
			return errors.New("executor requires coordinator_address and no listener_address")
		}
	} else {
		if network.CoordinatorAddress != "" {
			return errors.New("coordinator roles must not configure coordinator_address")
		}
		if config.Role == RoleCoordinator && !validHostPort(network.ListenerAddress) {
			return errors.New("coordinator requires listener_address")
		}
		if config.Role == RoleCombined && network.ListenerAddress != "" && !validHostPort(network.ListenerAddress) {
			return errors.New("listener_address is invalid")
		}
	}
	requiresTLS := network.CoordinatorAddress != "" || network.ListenerAddress != ""
	paths := []string{network.CertificateFile, network.PrivateKeyFile, network.TrustBundleFile}
	for _, path := range paths {
		if requiresTLS && !absolutePath(path) {
			return errors.New("network certificate, private key, and trust bundle references must be absolute")
		}
		if !requiresTLS && path != "" {
			return errors.New("network credential references require a configured address")
		}
	}
	return nil
}

func (config Config) validateRuntimePaths() error {
	if config.Paths == nil {
		return errors.New("runtime paths are required")
	}
	require := func(name, path string, required bool) error {
		if required && !absolutePath(path) {
			return fmt.Errorf("%s must be absolute", name)
		}
		if !required && path != "" {
			return fmt.Errorf("%s is not owned by role %s", name, config.Role)
		}
		return nil
	}
	if err := require("pairing_path", config.Paths.PairingPath, config.Coordinates()); err != nil {
		return err
	}
	if err := require("catalog_path", config.Paths.CatalogPath, config.Coordinates()); err != nil {
		return err
	}
	if err := require("fence_path", config.Paths.FencePath, config.Executes()); err != nil {
		return err
	}
	return require("ledger_path", config.Paths.LedgerPath, config.Executes())
}

func (config Config) validateUpdate() error {
	if config.Update == nil || !absolutePath(config.Update.TrustKeyFile) {
		return errors.New("update source and trust key reference are required")
	}
	parsed, err := url.Parse(config.Update.SourceURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return errors.New("update source_url must be a credential-free HTTPS URL")
	}
	return nil
}

func (config Config) validateBackup() error {
	if config.Backup == nil {
		return errors.New("backup configuration is required")
	}
	if config.Backup.Schedule != nil || config.Backup.Encryption != nil {
		return errors.New("backup schedule and encryption are intentionally unset")
	}
	if config.Backup.Destination != "" && !absolutePath(config.Backup.Destination) {
		return errors.New("backup destination must be an absolute owner-controlled path")
	}
	return nil
}
func (config Config) validateParakeet() error {
	if config.Parakeet == nil || !absolutePath(config.Parakeet.Executable) || !absolutePath(config.Parakeet.ModelPath) {
		return errors.New("Parakeet executable and model references must be absolute")
	}
	modelPlaceholderCount := 0
	for _, argument := range config.Parakeet.Argv {
		if strings.ContainsRune(argument, 0) {
			return errors.New("Parakeet argv contains NUL")
		}
		if argument == "{model_path}" {
			modelPlaceholderCount++
		}
	}
	if modelPlaceholderCount != 1 {
		return errors.New("Parakeet argv must contain exactly one model path placeholder")
	}
	return nil
}
func (config Config) validateMediaLimits() error {
	if config.MediaLimits == nil {
		return errors.New("media_limits are required")
	}
	values := []int64{
		config.MediaLimits.DownloadBytes, config.MediaLimits.UploadBytes,
		config.MediaLimits.VoiceBytes, config.MediaLimits.PhotoBytes,
		config.MediaLimits.TranscriptBytes, config.MediaLimits.DiagnosticBytes,
	}
	for _, value := range values {
		if value <= 0 || value > 1<<40 {
			return errors.New("media limits must be positive and bounded")
		}
	}
	if config.MediaLimits.VoiceBytes > config.MediaLimits.DownloadBytes ||
		config.MediaLimits.PhotoBytes > config.MediaLimits.DownloadBytes {
		return errors.New("voice and photo limits must not exceed download limit")
	}
	return nil
}

func (config Config) validateRuntimeFeatures() error {
	if config.Runtime == nil {
		return nil
	}
	if config.Runtime.P4 != nil {
		if config.Role != RoleCombined {
			return errors.New("runtime.p4 is owned only by the combined role")
		}
		p4 := config.Runtime.P4
		paths := []string{p4.VoiceTempDirectory, p4.PhotoCustodyDirectory, p4.ArtifactManifestDirectory, p4.ArtifactRetryDirectory, p4.ArtifactRetryKey.SecretFile}
		if !validRuntimePaths(paths...) || len(p4.ArtifactAllowedRoots) == 0 || len(p4.ArtifactAllowedRoots) > 64 || p4.ArtifactRetryTTLSeconds <= 0 || p4.ArtifactRetryTTLSeconds > 30*24*60*60 {
			return errors.New("runtime.p4 requires bounded explicit private paths, roots, retry key, and ttl")
		}
		for _, root := range p4.ArtifactAllowedRoots {
			if !validRuntimePath(root) {
				return errors.New("runtime.p4 artifact roots must be absolute")
			}
		}
	}
	if config.Runtime.Discovery != nil {
		if config.Role != RoleCombined {
			return errors.New("runtime.discovery is owned only by the combined role")
		}
		if !validRuntimePaths(config.Runtime.Discovery.CodexRoot, config.Runtime.Discovery.ClaudeRoot) {
			return errors.New("runtime.discovery requires exact Codex and Claude roots")
		}
	}
	if config.Runtime.Backup != nil {
		if config.Role != RoleCombined {
			return errors.New("runtime.backup is owned only by the combined role")
		}
		backup := config.Runtime.Backup
		if !validRuntimePaths(backup.WorkDirectory, backup.LatestPath, backup.RestoreCandidateDirectory, backup.LiveDirectory, backup.MarkerPath, backup.CodexHistoryRoot, backup.ClaudeHistoryRoot, backup.HarnessRoot) {
			return errors.New("runtime.backup requires explicit local paths")
		}
	}
	if config.Runtime.Update != nil {
		if !config.Executes() {
			return errors.New("runtime.update is owned only by an executing role")
		}
		update := config.Runtime.Update
		if !validRuntimePaths(update.StageDirectory, update.StateDirectory, update.InstallRoot, update.LockPath, update.VerifierPath, update.FingerprintPath, update.InstallExecutable, update.RollbackExecutable, update.ConfigFile, update.HealthProbePath) {
			return errors.New("runtime.update requires explicit staging, install, lock, verifier, and health paths")
		}
	}
	return config.validateRuntimePathSeparation()
}

func (config Config) validateRuntimePathSeparation() error {
	paths := config.PathReferences()
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	for left := 0; left < len(names); left++ {
		for right := left + 1; right < len(names); right++ {
			if runtimePathsOverlap(paths[names[left]], paths[names[right]]) {
				return fmt.Errorf("runtime paths %s and %s overlap", names[left], names[right])
			}
		}
	}
	return nil
}

func runtimePathsOverlap(left, right string) bool {
	if knownSamePath(left, right) {
		return true
	}
	leftInRight, leftErr := filepath.Rel(right, left)
	rightInLeft, rightErr := filepath.Rel(left, right)
	return leftErr == nil && !relativeOutside(leftInRight) || rightErr == nil && !relativeOutside(rightInLeft)
}

func relativeOutside(path string) bool {
	return path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator))
}

func validRuntimePaths(paths ...string) bool {
	for _, path := range paths {
		if !validRuntimePath(path) {
			return false
		}
	}
	return true
}

func validRuntimePath(path string) bool {
	return absolutePath(path) && filepath.Clean(path) == path
}

func (config Config) PathReferences() map[string]string {
	paths := map[string]string{"state_path": config.StatePath}
	add := func(name, path string) {
		if path != "" {
			paths[name] = path
		}
	}
	add("telegram_token.secret_file", config.TelegramToken.SecretFile)
	add("callback_key.secret_file", config.CallbackKey.SecretFile)
	if config.Network != nil {
		add("network.certificate_file", config.Network.CertificateFile)
		add("network.private_key_file", config.Network.PrivateKeyFile)
		add("network.trust_bundle_file", config.Network.TrustBundleFile)
	}
	if config.Paths != nil {
		add("paths.pairing_path", config.Paths.PairingPath)
		add("paths.catalog_path", config.Paths.CatalogPath)
		add("paths.fence_path", config.Paths.FencePath)
		add("paths.ledger_path", config.Paths.LedgerPath)
	}
	if config.Update != nil {
		add("update.trust_key_file", config.Update.TrustKeyFile)
	}
	if config.Backup != nil {
		add("backup.destination", config.Backup.Destination)
	}
	if config.Parakeet != nil {
		add("parakeet.executable", config.Parakeet.Executable)
		add("parakeet.model_path", config.Parakeet.ModelPath)
	}
	if config.Runtime != nil {
		if p4 := config.Runtime.P4; p4 != nil {
			add("runtime.p4.voice_temp_directory", p4.VoiceTempDirectory)
			add("runtime.p4.photo_custody_directory", p4.PhotoCustodyDirectory)
			add("runtime.p4.artifact_manifest_directory", p4.ArtifactManifestDirectory)
			add("runtime.p4.artifact_retry_directory", p4.ArtifactRetryDirectory)
			add("runtime.p4.artifact_retry_key.secret_file", p4.ArtifactRetryKey.SecretFile)
			for index, root := range p4.ArtifactAllowedRoots {
				add("runtime.p4.artifact_allowed_roots."+strconv.Itoa(index), root)
			}
		}
		if discovery := config.Runtime.Discovery; discovery != nil {
			add("runtime.discovery.codex_root", discovery.CodexRoot)
			add("runtime.discovery.claude_root", discovery.ClaudeRoot)
		}
		if backup := config.Runtime.Backup; backup != nil {
			add("runtime.backup.work_directory", backup.WorkDirectory)
			add("runtime.backup.latest_path", backup.LatestPath)
			add("runtime.backup.restore_candidate_directory", backup.RestoreCandidateDirectory)
			add("runtime.backup.live_directory", backup.LiveDirectory)
			add("runtime.backup.marker_path", backup.MarkerPath)
			add("runtime.backup.codex_history_root", backup.CodexHistoryRoot)
			add("runtime.backup.claude_history_root", backup.ClaudeHistoryRoot)
			add("runtime.backup.harness_root", backup.HarnessRoot)
		}
		if update := config.Runtime.Update; update != nil {
			add("runtime.update.stage_directory", update.StageDirectory)
			add("runtime.update.state_directory", update.StateDirectory)
			add("runtime.update.install_root", update.InstallRoot)
			add("runtime.update.lock_path", update.LockPath)
			add("runtime.update.verifier_path", update.VerifierPath)
			add("runtime.update.fingerprint_path", update.FingerprintPath)
			add("runtime.update.install_executable", update.InstallExecutable)
			add("runtime.update.rollback_executable", update.RollbackExecutable)
			add("runtime.update.config_file", update.ConfigFile)
			add("runtime.update.health_probe_path", update.HealthProbePath)
		}
	}
	for name, provider := range config.Providers {
		if provider.Command != nil {
			add("providers."+name+".command.exec", provider.Command.Exec)
		}
	}
	return paths
}

func validateNonOverlappingPaths(paths map[string]string) error {
	names := make([]string, 0, len(paths))
	for name, path := range paths {
		if !absolutePath(path) {
			return fmt.Errorf("%s must be absolute", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for left := 0; left < len(names); left++ {
		for right := left + 1; right < len(names); right++ {
			if knownSamePath(paths[names[left]], paths[names[right]]) {
				return fmt.Errorf("configuration paths %s and %s overlap", names[left], names[right])
			}
		}
	}
	return nil
}

func absolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && !strings.ContainsRune(path, 0)
}

func validHostPort(address string) bool {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(host) != host ||
		strings.ContainsAny(host, `@/\?#`) || hasControl(host) {
		return false
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port > 0 && port <= 65535
}

func hasControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

var computerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validateVersionedJSON(top map[string]json.RawMessage) error {
	allowed := []string{
		"version", "role", "computer", "owner_user_id", "private_chat_id", "bot_username",
		"state_path", "telegram_token", "callback_key", "network", "paths", "providers",
		"update", "backup", "parakeet", "media_limits", "runtime",
	}
	if err := rejectUnknownKeys(top, allowed...); err != nil {
		return err
	}
	for _, field := range []string{"version", "role", "computer", "state_path", "network", "paths", "providers", "update", "backup", "parakeet", "media_limits"} {
		if _, ok := top[field]; !ok {
			return fmt.Errorf("missing field %q", field)
		}
	}
	var role Role
	if err := json.Unmarshal(top["role"], &role); err != nil {
		return errors.New("role must be a string")
	}
	if role == RoleCombined || role == RoleCoordinator {
		for _, field := range []string{"owner_user_id", "private_chat_id", "bot_username", "telegram_token", "callback_key"} {
			if _, ok := top[field]; !ok {
				return fmt.Errorf("missing coordinator field %q", field)
			}
		}
	}
	objects := []struct {
		name     string
		allowed  []string
		required []string
	}{
		{"computer", []string{"id", "name"}, []string{"id", "name"}},
		{"network", []string{"coordinator_address", "listener_address", "certificate_file", "private_key_file", "trust_bundle_file"}, []string{"coordinator_address", "listener_address", "certificate_file", "private_key_file", "trust_bundle_file"}},
		{"paths", []string{"pairing_path", "catalog_path", "fence_path", "ledger_path"}, []string{"pairing_path", "catalog_path", "fence_path", "ledger_path"}},
		{"update", []string{"source_url", "trust_key_file"}, []string{"source_url", "trust_key_file"}},
		{"backup", []string{"destination", "schedule", "encryption"}, []string{"destination", "schedule", "encryption"}},
		{"parakeet", []string{"executable", "model_path", "argv"}, []string{"executable", "model_path", "argv"}},
		{"media_limits", []string{"download_bytes", "upload_bytes", "voice_bytes", "photo_bytes", "transcript_bytes", "diagnostic_bytes"}, []string{"download_bytes", "upload_bytes", "voice_bytes", "photo_bytes", "transcript_bytes", "diagnostic_bytes"}},
	}
	for _, spec := range objects {
		object, err := decodeJSONObject(top[spec.name])
		if err != nil {
			return fmt.Errorf("%s: %w", spec.name, err)
		}
		if err := rejectUnknownKeys(object, spec.allowed...); err != nil {
			return fmt.Errorf("%s: %w", spec.name, err)
		}
		for _, field := range spec.required {
			if _, ok := object[field]; !ok {
				return fmt.Errorf("%s: missing field %q", spec.name, field)
			}
		}
	}
	backup, _ := decodeJSONObject(top["backup"])
	for _, field := range []string{"schedule", "encryption"} {
		if !bytes.Equal(bytes.TrimSpace(backup[field]), []byte("null")) {
			return fmt.Errorf("backup.%s must be explicitly null", field)
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
		callback, err := decodeJSONObject(raw)
		if err != nil {
			return fmt.Errorf("callback_key: %w", err)
		}
		if err := rejectUnknownKeys(callback, "secret_file"); err != nil {
			return fmt.Errorf("callback_key: %w", err)
		}
	}
	if raw, ok := top["runtime"]; ok {
		if err := validateRuntimeFeaturesJSON(raw); err != nil {
			return fmt.Errorf("runtime: %w", err)
		}
	}
	return validateProvidersJSON(top["providers"])
}

func validateRuntimeFeaturesJSON(raw json.RawMessage) error {
	runtime, err := decodeJSONObject(raw)
	if err != nil {
		return err
	}
	if err := rejectUnknownKeys(runtime, "p4", "discovery", "backup", "update"); err != nil {
		return err
	}
	blocks := []struct {
		name   string
		fields []string
	}{
		{
			"p4",
			[]string{"voice_temp_directory", "photo_custody_directory", "artifact_manifest_directory", "artifact_retry_directory", "artifact_allowed_roots", "artifact_retry_key", "artifact_retry_ttl_seconds"},
		},
		{"discovery", []string{"codex_root", "claude_root"}},
		{"backup", []string{"work_directory", "latest_path", "restore_candidate_directory", "live_directory", "marker_path", "codex_history_root", "claude_history_root", "harness_root"}},
		{"update", []string{"stage_directory", "state_directory", "install_root", "lock_path", "verifier_path", "fingerprint_path", "install_executable", "rollback_executable", "config_file", "health_probe_path"}},
	}
	for _, block := range blocks {
		value, ok := runtime[block.name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		object, err := decodeJSONObject(value)
		if err != nil {
			return fmt.Errorf("%s: %w", block.name, err)
		}
		if err := rejectUnknownKeys(object, block.fields...); err != nil {
			return fmt.Errorf("%s: %w", block.name, err)
		}
		for _, field := range block.fields {
			if _, ok := object[field]; !ok {
				return fmt.Errorf("%s: missing field %q", block.name, field)
			}
		}
		if block.name == "p4" {
			retryKey, err := decodeJSONObject(object["artifact_retry_key"])
			if err != nil {
				return fmt.Errorf("p4.artifact_retry_key: %w", err)
			}
			if err := rejectUnknownKeys(retryKey, "secret_file"); err != nil {
				return fmt.Errorf("p4.artifact_retry_key: %w", err)
			}
			if _, ok := retryKey["secret_file"]; !ok {
				return errors.New("p4.artifact_retry_key: missing field \"secret_file\"")
			}
		}
	}
	return nil
}

func validateProvidersJSON(raw json.RawMessage) error {
	providers, err := decodeJSONObject(raw)
	if err != nil {
		return fmt.Errorf("providers: %w", err)
	}
	for name, rawProvider := range providers {
		if name != domainProviderCodex && name != domainProviderClaude {
			return errors.New("unsupported provider")
		}
		provider, err := decodeJSONObject(rawProvider)
		if err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		if err := rejectUnknownKeys(provider, "enabled", "command"); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		if _, ok := provider["enabled"]; !ok {
			return fmt.Errorf("provider %q: enabled must be explicitly specified", name)
		}
		commandRaw, ok := provider["command"]
		if !ok || bytes.Equal(bytes.TrimSpace(commandRaw), []byte("null")) {
			continue
		}
		command, err := decodeJSONObject(commandRaw)
		if err != nil {
			return fmt.Errorf("provider %q command: %w", name, err)
		}
		if err := rejectUnknownKeys(command, "exec", "argv"); err != nil {
			return fmt.Errorf("provider %q command: %w", name, err)
		}
	}
	return nil
}
