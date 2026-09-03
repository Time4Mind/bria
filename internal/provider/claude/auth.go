package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bria/internal/authflow"
	"bria/internal/runtimeprotocol"
)

const (
	claudeAuthLifetime      = 15 * time.Minute
	claudeCredentialVersion = 1
	maxClaudeAPIKeyBytes    = 64 << 10
	maxClaudeCredentialFile = 128 << 10
	anthropicAPIKeyEnv      = "ANTHROPIC_API_KEY"
	// CredentialFileEnvironment carries only a private local-file reference.
	// The API key itself is never stored in this environment value.
	CredentialFileEnvironment = runtimeprotocol.EnvironmentProviderCredentialFile
)

var ErrClaudeCredential = errors.New("Claude API key credential is unavailable")
var claudeCredentialLocks sync.Map

type apiKeyVerifier interface {
	Verify(context.Context, []byte) error
}

// APIKeyAuthenticator implements the Telegram-facing flow with Claude's
// documented API-key-only --bare mode. It never automates browser OAuth,
// setup-token, or Claude subscription authentication.
type APIKeyAuthenticator struct {
	verifier       apiKeyVerifier
	credentialPath string
	now            func() time.Time
}

var _ authflow.Authenticator = (*APIKeyAuthenticator)(nil)

func NewAPIKeyAuthenticator(claudePath, credentialPath string) (*APIKeyAuthenticator, error) {
	verifier, err := newPinnedBareVerifier(claudePath)
	if err != nil {
		return nil, ErrClaudeCredential
	}
	return newAPIKeyAuthenticator(verifier, credentialPath, time.Now)
}

func newAPIKeyAuthenticator(verifier apiKeyVerifier, credentialPath string, now func() time.Time) (*APIKeyAuthenticator, error) {
	if verifier == nil || now == nil || !filepath.IsAbs(credentialPath) || strings.ContainsRune(credentialPath, 0) {
		return nil, ErrClaudeCredential
	}
	credentialPath = filepath.Clean(credentialPath)
	lock := claudeCredentialLock(credentialPath)
	lock.Lock()
	defer lock.Unlock()
	if _, _, err := readClaudeCredential(credentialPath); err != nil {
		return nil, err
	}
	return &APIKeyAuthenticator{verifier: verifier, credentialPath: credentialPath, now: now}, nil
}

func (auth *APIKeyAuthenticator) Begin(ctx context.Context, request authflow.BeginRequest) (authflow.BeginResult, error) {
	if ctx == nil || ctx.Err() != nil || request.Provider != authflow.ProviderClaude || !validClaudeAuthID(request.OperationID) || !validClaudeAuthID(request.ComputerID) {
		return authflow.BeginResult{}, authflow.ErrAuthorizationUnconfirmed
	}
	return authflow.BeginResult{
		ChallengeReference: claudeAPIKeyChallenge(request.OperationID, request.ComputerID),
		Instruction:        "Отправьте API key Claude одним сообщением. Bria проверит его официальным Claude CLI в режиме --bare и удалит сообщение.",
		ExpiresAt:          auth.now().UTC().Add(claudeAuthLifetime),
	}, nil
}

func (auth *APIKeyAuthenticator) Complete(ctx context.Context, request authflow.CompleteRequest) error {
	if ctx == nil || ctx.Err() != nil || request.Provider != authflow.ProviderClaude || !validClaudeAuthID(request.OperationID) || !validClaudeAuthID(request.ComputerID) || request.ChallengeReference != claudeAPIKeyChallenge(request.OperationID, request.ComputerID) {
		return authflow.ErrAuthorizationUnconfirmed
	}
	secret := request.Secret.Bytes()
	defer zeroClaudeBytes(secret)
	if !validClaudeAPIKey(secret) {
		return authflow.ErrAuthorizationUnconfirmed
	}
	lock := claudeCredentialLock(auth.credentialPath)
	lock.Lock()
	defer lock.Unlock()
	current, found, err := readClaudeCredential(auth.credentialPath)
	if err != nil {
		return ErrClaudeCredential
	}
	if found && current.OperationID == request.OperationID && current.ComputerID == request.ComputerID {
		defer zeroClaudeBytes(current.APIKey)
		if subtle.ConstantTimeCompare(current.APIKey, secret) == 1 {
			return nil
		}
		return authflow.ErrAuthorizationUnconfirmed
	}
	if err := auth.verifier.Verify(ctx, secret); err != nil {
		if errors.Is(err, authflow.ErrProviderRejected) {
			return authflow.ErrProviderRejected
		}
		return authflow.ErrAuthorizationUnconfirmed
	}
	record := claudeCredential{Version: claudeCredentialVersion, OperationID: request.OperationID, ComputerID: request.ComputerID, APIKey: append([]byte(nil), secret...)}
	defer zeroClaudeBytes(record.APIKey)
	if err := writeClaudeCredential(auth.credentialPath, record); err != nil {
		return ErrClaudeCredential
	}
	verified, found, err := readClaudeCredential(auth.credentialPath)
	if err != nil || !found {
		return ErrClaudeCredential
	}
	defer zeroClaudeBytes(verified.APIKey)
	if verified.Version != record.Version || verified.OperationID != record.OperationID || verified.ComputerID != record.ComputerID || subtle.ConstantTimeCompare(verified.APIKey, record.APIKey) != 1 {
		return ErrClaudeCredential
	}
	return nil
}

func claudeAPIKeyChallenge(operationID, computerID string) string {
	sum := sha256.Sum256([]byte(operationID + "\x00" + computerID))
	return "claude-api-key:" + hex.EncodeToString(sum[:16])
}

type claudeCredential struct {
	Version     int    `json:"version"`
	OperationID string `json:"operation_id"`
	ComputerID  string `json:"computer_id"`
	APIKey      []byte `json:"api_key"`
}

func readClaudeCredential(path string) (claudeCredential, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return claudeCredential{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxClaudeCredentialFile {
		return claudeCredential{}, false, ErrClaudeCredential
	}
	file, err := os.Open(path)
	if err != nil {
		return claudeCredential{}, false, ErrClaudeCredential
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxClaudeCredentialFile+1))
	if err != nil || len(data) > maxClaudeCredentialFile {
		zeroClaudeBytes(data)
		return claudeCredential{}, false, ErrClaudeCredential
	}
	defer zeroClaudeBytes(data)
	var record claudeCredential
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return claudeCredential{}, false, ErrClaudeCredential
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		zeroClaudeBytes(record.APIKey)
		return claudeCredential{}, false, ErrClaudeCredential
	}
	if record.Version != claudeCredentialVersion || strings.TrimSpace(record.OperationID) == "" || strings.TrimSpace(record.ComputerID) == "" || !validClaudeAPIKey(record.APIKey) {
		zeroClaudeBytes(record.APIKey)
		return claudeCredential{}, false, ErrClaudeCredential
	}
	return record, true, nil
}

func writeClaudeCredential(path string, record claudeCredential) error {
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxClaudeCredentialFile {
		zeroClaudeBytes(data)
		return ErrClaudeCredential
	}
	defer zeroClaudeBytes(data)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrClaudeCredential
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return ErrClaudeCredential
	}
	temporary, err := os.CreateTemp(directory, ".claude-credential-*")
	if err != nil {
		return ErrClaudeCredential
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		if open {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if temporary.Chmod(0o600) != nil || writeAndSync(temporary, data) != nil || temporary.Close() != nil {
		return ErrClaudeCredential
	}
	open = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return ErrClaudeCredential
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return ErrClaudeCredential
	}
	syncErr := directoryHandle.Sync()
	closeErr := directoryHandle.Close()
	if syncErr != nil || closeErr != nil {
		return ErrClaudeCredential
	}
	return nil
}

func writeAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func claudeCredentialLock(path string) *sync.Mutex {
	value, _ := claudeCredentialLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func validClaudeAPIKey(secret []byte) bool {
	if len(secret) == 0 || len(secret) > maxClaudeAPIKeyBytes || !utf8.Valid(secret) || bytes.IndexAny(secret, "\x00\r\n\t ") >= 0 {
		return false
	}
	for _, character := range secret {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func validClaudeAuthID(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func zeroClaudeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type pinnedBareVerifier struct {
	path        string
	identity    os.FileInfo
	environment []string
}

func newPinnedBareVerifier(path string) (*pinnedBareVerifier, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, ErrClaudeCredential
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, ErrClaudeCredential
	}
	info, err := os.Stat(resolved)
	if err != nil || !secureClaudeExecutable(info) {
		return nil, ErrClaudeCredential
	}
	environment, err := minimalClaudeVerifierEnvironment(os.Environ())
	if err != nil {
		return nil, ErrClaudeCredential
	}
	return &pinnedBareVerifier{path: resolved, identity: info, environment: environment}, nil
}

func minimalClaudeVerifierEnvironment(parent []string) ([]string, error) {
	allowed := map[string]struct{}{
		"HOME": {}, "PATH": {}, "TMPDIR": {}, "TMP": {}, "TEMP": {},
		"USERPROFILE": {}, "SYSTEMROOT": {}, "COMSPEC": {}, "PATHEXT": {}, "WINDIR": {},
		"LANG": {}, "LC_ALL": {}, "LC_CTYPE": {},
		"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {},
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "NO_PROXY": {}, "ALL_PROXY": {},
	}
	result := make([]string, 0, len(allowed))
	for _, entry := range parent {
		index := strings.IndexByte(entry, '=')
		if index <= 0 || strings.ContainsRune(entry[index+1:], 0) {
			return nil, ErrClaudeCredential
		}
		key := entry[:index]
		lookup := key
		if runtime.GOOS == "windows" {
			lookup = strings.ToUpper(key)
		}
		if _, ok := allowed[lookup]; ok {
			result = append(result, entry)
		}
	}
	return result, nil
}

func (verifier *pinnedBareVerifier) Verify(ctx context.Context, secret []byte) error {
	if ctx == nil || ctx.Err() != nil || !validClaudeAPIKey(secret) || !verifier.matches() {
		return authflow.ErrAuthorizationUnconfirmed
	}
	home, err := os.MkdirTemp("", ".bria-claude-auth-")
	if err != nil {
		return authflow.ErrAuthorizationUnconfirmed
	}
	defer os.RemoveAll(home)
	if err := os.Chmod(home, 0o700); err != nil {
		return authflow.ErrAuthorizationUnconfirmed
	}
	configDirectory := filepath.Join(home, "config")
	if err := os.Mkdir(configDirectory, 0o700); err != nil {
		return authflow.ErrAuthorizationUnconfirmed
	}
	environment, err := isolatedClaudeVerifierEnvironment(verifier.environment, home, configDirectory)
	if err != nil {
		return authflow.ErrAuthorizationUnconfirmed
	}
	environment, err = claudeAPIKeyEnvironment(environment, secret)
	if err != nil {
		return authflow.ErrAuthorizationUnconfirmed
	}
	defer redactClaudeEnvironment(environment)
	command := exec.CommandContext(ctx, verifier.path,
		"--bare", "--print", "--input-format", "stream-json", "--output-format", "stream-json",
		"--tools", "", "--max-turns", "1", "--no-session-persistence",
	)
	command.Env = environment
	command.Dir = home
	command.Stdin = strings.NewReader(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Reply with exactly OK."}]}}` + "\n")
	output := &boundedClaudeAuthOutput{limit: maxClaudeCredentialFile}
	command.Stdout = output
	command.Stderr = io.Discard
	runErr := command.Run()
	return classifyClaudeAuthOutput(output.data, runErr)
}

func isolatedClaudeVerifierEnvironment(parent []string, home, configDirectory string) ([]string, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(configDirectory) || strings.ContainsRune(home, 0) || strings.ContainsRune(configDirectory, 0) {
		return nil, ErrClaudeCredential
	}
	result := make([]string, 0, len(parent)+5)
	for _, entry := range parent {
		index := strings.IndexByte(entry, '=')
		if index <= 0 || strings.ContainsRune(entry[index+1:], 0) {
			return nil, ErrClaudeCredential
		}
		key := entry[:index]
		lookup := key
		if runtime.GOOS == "windows" {
			lookup = strings.ToUpper(key)
		}
		switch lookup {
		case "HOME", "USERPROFILE", "CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA":
			continue
		}
		result = append(result, entry)
	}
	result = append(result,
		"HOME="+home,
		"USERPROFILE="+home,
		"CLAUDE_CONFIG_DIR="+configDirectory,
		"XDG_CONFIG_HOME="+configDirectory,
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
	)
	return result, nil
}

func (verifier *pinnedBareVerifier) matches() bool {
	info, err := os.Stat(verifier.path)
	return err == nil && secureClaudeExecutable(info) && os.SameFile(info, verifier.identity)
}

func claudeAPIKeyEnvironment(parent []string, secret []byte) ([]string, error) {
	if !validClaudeAPIKey(secret) {
		return nil, ErrClaudeCredential
	}
	result, err := withoutAnthropicAPIKey(parent)
	if err != nil {
		return nil, err
	}
	result = append(result, anthropicAPIKeyEnv+"="+string(secret))
	return result, nil
}

func withoutAnthropicAPIKey(parent []string) ([]string, error) {
	result := make([]string, 0, len(parent))
	for _, entry := range parent {
		index := strings.IndexByte(entry, '=')
		if index <= 0 || strings.ContainsRune(entry[index+1:], 0) {
			return nil, ErrClaudeCredential
		}
		key := entry[:index]
		matches := key == anthropicAPIKeyEnv || key == CredentialFileEnvironment
		if runtime.GOOS == "windows" {
			matches = strings.EqualFold(key, anthropicAPIKeyEnv) || strings.EqualFold(key, CredentialFileEnvironment)
		}
		if !matches {
			result = append(result, entry)
		}
	}
	return result, nil
}

func redactClaudeEnvironment(environment []string) {
	for index, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && (key == anthropicAPIKeyEnv || runtime.GOOS == "windows" && strings.EqualFold(key, anthropicAPIKeyEnv)) {
			environment[index] = anthropicAPIKeyEnv + "=[REDACTED]"
		}
	}
}

type boundedClaudeAuthOutput struct {
	data  []byte
	limit int
}

func (output *boundedClaudeAuthOutput) Write(value []byte) (int, error) {
	if len(output.data)+len(value) > output.limit {
		return 0, ErrClaudeCredential
	}
	output.data = append(output.data, value...)
	return len(value), nil
}

func classifyClaudeAuthOutput(data []byte, runErr error) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	successes := 0
	rejected := false
	for scanner.Scan() {
		var event struct {
			Type           string `json:"type"`
			Error          string `json:"error"`
			IsError        *bool  `json:"is_error"`
			TerminalReason string `json:"terminal_reason"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return authflow.ErrAuthorizationUnconfirmed
		}
		if event.Type == "assistant" && event.Error == string(FailureAuthentication) {
			rejected = true
		}
		if event.Type == "result" {
			if event.IsError == nil {
				return authflow.ErrAuthorizationUnconfirmed
			}
			if !*event.IsError && event.TerminalReason == "completed" {
				successes++
			}
		}
	}
	if scanner.Err() != nil {
		return authflow.ErrAuthorizationUnconfirmed
	}
	if rejected {
		return authflow.ErrProviderRejected
	}
	if runErr == nil && successes == 1 {
		return nil
	}
	return authflow.ErrAuthorizationUnconfirmed
}

// EnvironmentWithStoredAPIKey will return an environment for this exact
// pinned Claude child without mutating the adapter process environment.
func (spec CommandSpec) EnvironmentWithStoredAPIKey(parent []string, credentialPath string) ([]string, error) {
	if !spec.ExecutableMatches() || !filepath.IsAbs(credentialPath) || strings.ContainsRune(credentialPath, 0) {
		return nil, ErrClaudeCredential
	}
	credentialPath = filepath.Clean(credentialPath)
	lock := claudeCredentialLock(credentialPath)
	lock.Lock()
	defer lock.Unlock()
	record, found, err := readClaudeCredential(credentialPath)
	if err != nil || !found {
		return nil, ErrClaudeCredential
	}
	defer zeroClaudeBytes(record.APIKey)
	environment, err := claudeAPIKeyEnvironment(parent, record.APIKey)
	if err != nil {
		return nil, ErrClaudeCredential
	}
	return environment, nil
}
