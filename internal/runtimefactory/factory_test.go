package runtimefactory_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/provider/codex"
	"bria/internal/runtimefactory"
	"bria/internal/sessionruntime"
)

func TestMain(main *testing.M) {
	if os.Getenv("RUNTIMEFACTORY_CODEX_GRANDCHILD") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("RUNTIMEFACTORY_REAL_CODEX") == "1" {
		runRealCodexFixture()
		os.Exit(0)
	}
	if os.Getenv("RUNTIMEFACTORY_HELPER") == "1" {
		runAdapterHelper()
		os.Exit(0)
	}
	os.Exit(main.Run())
}

func runRealCodexFixture() {
	if len(os.Args) > 2 && os.Args[1] == "--" {
		workdir, err := os.Getwd()
		if err != nil {
			os.Exit(90)
		}
		if pidPath := os.Getenv("RUNTIMEFACTORY_ADAPTER_PID_PATH"); pidPath != "" {
			if os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600) != nil {
				os.Exit(101)
			}
		}
		err = codex.RunAdapter(context.Background(), os.Stdin, os.Stdout, codex.AdapterConfig{
			RawCommand: os.Args[2:], RawEnv: os.Environ(), Workdir: workdir,
			ClientInfo: codex.ClientInfo{Name: "runtimefactory-nested-test", Version: "test"},
		})
		if err != nil {
			os.Exit(91)
		}
		return
	}
	if len(os.Args) != 2 || os.Args[1] != "app-server" {
		os.Exit(92)
	}
	grandchild := exec.Command(os.Args[0])
	grandchild.Env = append(os.Environ(), "RUNTIMEFACTORY_CODEX_GRANDCHILD=1")
	if grandchild.Start() != nil {
		os.Exit(93)
	}
	pids := strconv.Itoa(os.Getpid()) + " " + strconv.Itoa(grandchild.Process.Pid)
	if os.WriteFile(os.Getenv("RUNTIMEFACTORY_GRANDCHILD_PID_PATH"), []byte(pids), 0o600) != nil {
		_ = grandchild.Process.Kill()
		os.Exit(94)
	}
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	request := readRuntimefactoryRawObject(reader)
	if request["method"] != "initialize" {
		os.Exit(95)
	}
	_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{"userAgent": "fixture"}})
	request = readRuntimefactoryRawObject(reader)
	if request["method"] != "initialized" {
		os.Exit(96)
	}
	request = readRuntimefactoryRawObject(reader)
	if request["method"] != "thread/start" {
		os.Exit(97)
	}
	_ = encoder.Encode(map[string]any{"id": request["id"], "result": map[string]any{
		"thread": map[string]any{"id": "nested-thread"},
	}})
	_, _ = io.Copy(io.Discard, reader)
}

func readRuntimefactoryRawObject(reader *bufio.Reader) map[string]any {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		os.Exit(98)
	}
	var value map[string]any
	if json.Unmarshal(line, &value) != nil {
		os.Exit(99)
	}
	return value
}

func TestFactoryWrapsOnlyEnabledProviderWithSiblingAdapterAndSafeEnvironment(t *testing.T) {
	installDir := t.TempDir()
	briaExecutable := executableFixture(t, installDir, "bria", false)
	codexAdapter := executableFixture(t, installDir, "bria-codex-adapter", true)
	rawCodex := executableFixture(t, installDir, "raw-codex", false)
	configuration := validConfig(t, installDir)
	configuration.TelegramToken = config.TelegramTokenRef{EnvVar: "RUNTIMEFACTORY_TELEGRAM_TOKEN"}
	configuration.Providers["codex"] = config.ProviderConfig{
		Enabled: true,
		Command: &config.ProviderCommand{Exec: rawCodex, Argv: []string{"app-server"}},
	}
	configuration.Providers["claude"] = config.ProviderConfig{Enabled: false}

	parentEnvironment := testEnvironment(
		"RUNTIMEFACTORY_HELPER=1",
		"RUNTIMEFACTORY_EXPECT_ADAPTER="+codexAdapter,
		"RUNTIMEFACTORY_EXPECT_RAW="+rawCodex,
		"RUNTIMEFACTORY_EXPECT_PROVIDER=codex",
		"RUNTIMEFACTORY_TELEGRAM_TOKEN=top-secret-value",
		"RUNTIMEFACTORY_SAFE=preserved",
		"BRIA_PARENT_SECRET=must-be-stripped",
	)
	starter, err := runtimefactory.NewStarter(
		configuration,
		parentEnvironment,
		briaExecutable,
		sessionruntime.Options{HandshakeTimeout: 5 * time.Second, GracefulCloseTimeout: time.Second},
	)
	if err != nil {
		t.Fatalf("NewStarter() error = %v", err)
	}

	request := app.StartSessionRequest{
		SessionID: "logical-1", ComputerID: "local", Provider: domain.ProviderCodex, Workdir: t.TempDir(),
		Mode: app.SessionStartNew,
	}
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if binding.Provider != domain.ProviderCodex || binding.SessionID != "factory-provider-session" {
		t.Fatalf("Start() binding = %#v", binding)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}

	disabledRequest := request
	disabledRequest.SessionID = "logical-disabled"
	disabledRequest.Provider = domain.ProviderClaude
	if _, err := starter.Start(context.Background(), disabledRequest); err == nil {
		t.Fatal("Start(disabled Claude) error = nil")
	}
}

func TestCommandSetExposesExactImmutableAdapterSpecsForRuntimeAndRecovery(t *testing.T) {
	installDir := t.TempDir()
	briaExecutable := executableFixture(t, installDir, "bria", false)
	codexAdapter := executableFixture(t, installDir, "bria-codex-adapter", false)
	claudeAdapter := executableFixture(t, installDir, "bria-claude-adapter", false)
	rawCodex := executableFixture(t, installDir, "raw-codex", false)
	rawClaude := executableFixture(t, installDir, "raw-claude", false)
	configuration := validConfig(t, installDir)
	configuration.Providers["codex"] = config.ProviderConfig{Enabled: true, Command: &config.ProviderCommand{
		Exec: rawCodex, Argv: []string{"app-server", "--safe-codex"},
	}}
	configuration.Providers["claude"] = config.ProviderConfig{Enabled: true, Command: &config.ProviderCommand{
		Exec: rawClaude, Argv: []string{"--safe-claude"},
	}}
	commands, err := runtimefactory.NewCommandSet(configuration, testEnvironment(
		"RUNTIMEFACTORY_SAFE=preserved",
		"BRIA_PARENT_SECRET=must-not-enter-command",
	), briaExecutable)
	if err != nil {
		t.Fatalf("NewCommandSet() error = %v", err)
	}

	codexSpec, ok := commands.CommandSpec(domain.ProviderCodex)
	if !ok || codexSpec.Path != codexAdapter || !reflect.DeepEqual(codexSpec.Args, []string{"--", rawCodex, "app-server", "--safe-codex"}) || codexSpec.ProviderCredentialFile != "" {
		t.Fatalf("Codex CommandSpec = %#v, ok=%v", codexSpec, ok)
	}
	claudeSpec, ok := commands.CommandSpec(domain.ProviderClaude)
	wantCredential := configuration.StatePath + ".claude-api-key.json"
	if !ok || claudeSpec.Path != claudeAdapter || !reflect.DeepEqual(claudeSpec.Args, []string{"--", rawClaude, "--safe-claude"}) || claudeSpec.ProviderCredentialFile != wantCredential {
		t.Fatalf("Claude CommandSpec = %#v, ok=%v", claudeSpec, ok)
	}
	for _, value := range append(append([]string(nil), codexSpec.Env...), claudeSpec.Env...) {
		if strings.Contains(value, "must-not-enter-command") {
			t.Fatalf("command environment leaked parent secret")
		}
	}
	wantCodexEnv := append([]string(nil), codexSpec.Env...)

	codexSpec.Args[0] = "mutated"
	if len(codexSpec.Env) > 0 {
		codexSpec.Env[0] = "MUTATED=1"
	}
	configuration.Providers["codex"].Command.Argv[0] = "mutated-source"
	again, ok := commands.CommandSpec(domain.ProviderCodex)
	if !ok || !reflect.DeepEqual(again.Args, []string{"--", rawCodex, "app-server", "--safe-codex"}) || !reflect.DeepEqual(again.Env, wantCodexEnv) {
		t.Fatalf("CommandSpec was mutable through returned/source slices: %#v", again)
	}
	if _, ok := commands.CommandSpec("unknown"); ok {
		t.Fatal("unknown provider exposed a command")
	}
	if starter, err := commands.NewStarter(sessionruntime.Options{}); err != nil || starter == nil {
		t.Fatalf("CommandSet.NewStarter() = %v, %v", starter, err)
	}
}

func TestConfiguredCommandSetIncludesDisabledConfiguredAndPreservesEnabledSpecs(t *testing.T) {
	installDir := t.TempDir()
	briaExecutable := executableFixture(t, installDir, "bria", false)
	executableFixture(t, installDir, "bria-codex-adapter", false)
	executableFixture(t, installDir, "bria-claude-adapter", false)
	rawCodex := executableFixture(t, installDir, "raw-codex", false)
	rawClaude := executableFixture(t, installDir, "raw-claude", false)
	configuration := validConfig(t, installDir)
	configuration.Providers["codex"] = config.ProviderConfig{Enabled: true, Command: &config.ProviderCommand{Exec: rawCodex, Argv: []string{"app-server"}}}
	configuration.Providers["claude"] = config.ProviderConfig{Enabled: false, Command: &config.ProviderCommand{Exec: rawClaude, Argv: []string{"--safe"}}}
	parent := testEnvironment("RUNTIMEFACTORY_SAFE=preserved", "BRIA_PRIVATE_VALUE=secret-value")

	enabled, err := runtimefactory.NewCommandSet(configuration, parent, briaExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := enabled.CommandSpec(domain.ProviderClaude); ok {
		t.Fatal("NewCommandSet exposed disabled Claude")
	}
	configured, err := runtimefactory.NewConfiguredCommandSet(configuration, parent, briaExecutable)
	if err != nil {
		t.Fatalf("NewConfiguredCommandSet() error = %v", err)
	}
	wantCodex, _ := enabled.CommandSpec(domain.ProviderCodex)
	gotCodex, ok := configured.CommandSpec(domain.ProviderCodex)
	if !ok || !reflect.DeepEqual(gotCodex, wantCodex) {
		t.Fatalf("enabled Codex changed: got=%#v want=%#v", gotCodex, wantCodex)
	}
	claude, ok := configured.CommandSpec(domain.ProviderClaude)
	if !ok || !reflect.DeepEqual(claude.Args, []string{"--", rawClaude, "--safe"}) {
		t.Fatalf("disabled configured Claude = %#v, ok=%v", claude, ok)
	}
	for _, entry := range claude.Env {
		if strings.Contains(entry, "secret-value") {
			t.Fatal("configured command environment leaked secret")
		}
	}
	wantClaudeArgs := append([]string(nil), claude.Args...)
	wantClaudeEnv := append([]string(nil), claude.Env...)
	claude.Args[0] = "mutated"
	if len(claude.Env) > 0 {
		claude.Env[0] = "MUTATED=1"
	}
	again, _ := configured.CommandSpec(domain.ProviderClaude)
	if !reflect.DeepEqual(again.Args, wantClaudeArgs) || !reflect.DeepEqual(again.Env, wantClaudeEnv) {
		t.Fatalf("configured command was mutable: %#v", again)
	}
}

func TestConfiguredCommandSetLeavesUnconfiguredAbsentAndRejectsUnsafeExecutable(t *testing.T) {
	installDir := t.TempDir()
	briaExecutable := executableFixture(t, installDir, "bria", false)
	executableFixture(t, installDir, "bria-claude-adapter", false)
	rawClaude := executableFixture(t, installDir, "raw-claude", false)
	configuration := validConfig(t, installDir)
	configuration.Providers["claude"] = config.ProviderConfig{Enabled: false, Command: &config.ProviderCommand{Exec: rawClaude, Argv: []string{}}}
	configured, err := runtimefactory.NewConfiguredCommandSet(configuration, testEnvironment(), briaExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := configured.CommandSpec(domain.ProviderCodex); ok {
		t.Fatal("unconfigured Codex exposed a command")
	}

	executableFixture(t, installDir, "bria-codex-adapter", false)
	privatePath := filepath.Join(installDir, "private-missing-provider")
	configuration.Providers["codex"] = config.ProviderConfig{Enabled: false, Command: &config.ProviderCommand{Exec: privatePath, Argv: []string{}}}
	_, err = runtimefactory.NewConfiguredCommandSet(configuration, testEnvironment("BRIA_PRIVATE_VALUE=secret-value"), briaExecutable)
	if !errors.Is(err, runtimefactory.ErrExecutable) {
		t.Fatalf("missing provider executable error = %v", err)
	}
	if strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unsafe executable error leaked private input: %v", err)
	}
}

func TestFactoryPassesOnlyClaudeCredentialReferenceToClaudeAdapter(t *testing.T) {
	installDir := t.TempDir()
	briaExecutable := executableFixture(t, installDir, "bria", false)
	claudeAdapter := executableFixture(t, installDir, "bria-claude-adapter", true)
	rawClaude := executableFixture(t, installDir, "raw-claude", false)
	configuration := validConfig(t, installDir)
	configuration.Providers["claude"] = config.ProviderConfig{Enabled: true, Command: &config.ProviderCommand{Exec: rawClaude, Argv: []string{"--safe-option"}}}
	credentialPath := configuration.StatePath + ".claude-api-key.json"
	parent := testEnvironment(
		"RUNTIMEFACTORY_HELPER=1",
		"RUNTIMEFACTORY_EXPECT_ADAPTER="+claudeAdapter,
		"RUNTIMEFACTORY_EXPECT_RAW="+rawClaude,
		"RUNTIMEFACTORY_EXPECT_PROVIDER=claude",
		"RUNTIMEFACTORY_EXPECT_ARG=--safe-option",
		"RUNTIMEFACTORY_EXPECT_CLAUDE_CREDENTIAL="+credentialPath,
		"RUNTIMEFACTORY_SAFE=preserved",
	)
	starter, err := runtimefactory.NewStarter(configuration, parent, briaExecutable, sessionruntime.Options{HandshakeTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request := app.StartSessionRequest{SessionID: "claude-logical", ComputerID: "local", Provider: domain.ProviderClaude, Workdir: t.TempDir(), Mode: app.SessionStartNew}
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryUsesNoTelegramEnvironmentNameForSecretFileReference(t *testing.T) {
	installDir := t.TempDir()
	briaExecutable := executableFixture(t, installDir, "bria", false)
	codexAdapter := executableFixture(t, installDir, "bria-codex-adapter", true)
	rawCodex := executableFixture(t, installDir, "raw-codex", false)
	configuration := validConfig(t, installDir)
	configuration.TelegramToken = config.TelegramTokenRef{SecretFile: filepath.Join(installDir, "telegram-token")}
	configuration.Providers["codex"] = config.ProviderConfig{
		Enabled: true,
		Command: &config.ProviderCommand{Exec: rawCodex, Argv: []string{"app-server"}},
	}
	configuration.Providers["claude"] = config.ProviderConfig{Enabled: false}

	parentEnvironment := testEnvironment(
		"RUNTIMEFACTORY_HELPER=1",
		"RUNTIMEFACTORY_EXPECT_ADAPTER="+codexAdapter,
		"RUNTIMEFACTORY_EXPECT_RAW="+rawCodex,
		"RUNTIMEFACTORY_EXPECT_PROVIDER=codex",
		"RUNTIMEFACTORY_EXPECT_TOKEN_PRESERVED=1",
		"RUNTIMEFACTORY_TELEGRAM_TOKEN=unrelated-value",
		"RUNTIMEFACTORY_SAFE=preserved",
	)
	starter, err := runtimefactory.NewStarter(configuration, parentEnvironment, briaExecutable, sessionruntime.Options{})
	if err != nil {
		t.Fatalf("NewStarter() error = %v", err)
	}
	request := app.StartSessionRequest{
		SessionID: "logical-file-token", ComputerID: "local", Provider: domain.ProviderCodex, Workdir: t.TempDir(),
		Mode: app.SessionStartNew,
	}
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
}

func TestFactoryWrapsClaudeWithClaudeSiblingAdapter(t *testing.T) {
	installDir := t.TempDir()
	briaExecutable := executableFixture(t, installDir, "bria", false)
	claudeAdapter := executableFixture(t, installDir, "bria-claude-adapter", true)
	rawClaude := executableFixture(t, installDir, "raw-claude", false)
	configuration := validConfig(t, installDir)
	configuration.Providers["codex"] = config.ProviderConfig{Enabled: false}
	configuration.Providers["claude"] = config.ProviderConfig{
		Enabled: true,
		Command: &config.ProviderCommand{Exec: rawClaude, Argv: []string{"--print"}},
	}
	starter, err := runtimefactory.NewStarter(configuration, testEnvironment(
		"RUNTIMEFACTORY_HELPER=1",
		"RUNTIMEFACTORY_EXPECT_ADAPTER="+claudeAdapter,
		"RUNTIMEFACTORY_EXPECT_RAW="+rawClaude,
		"RUNTIMEFACTORY_EXPECT_ARG=--print",
		"RUNTIMEFACTORY_EXPECT_PROVIDER=claude",
		"RUNTIMEFACTORY_SAFE=preserved",
	), briaExecutable, sessionruntime.Options{})
	if err != nil {
		t.Fatalf("NewStarter() error = %v", err)
	}
	request := app.StartSessionRequest{
		SessionID: "logical-claude", ComputerID: "local", Provider: domain.ProviderClaude, Workdir: t.TempDir(),
		Mode: app.SessionStartNew,
	}
	binding, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if binding.Provider != domain.ProviderClaude {
		t.Fatalf("Start() binding = %#v", binding)
	}
	if err := starter.Abort(context.Background(), request, binding); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
}

func TestFactoryRejectsMissingOrUnsafeSiblingWithoutPathDiscoveryOrSecretLeak(t *testing.T) {
	tests := []struct {
		name        string
		makeAdapter func(*testing.T, string)
	}{
		{name: "missing"},
		{name: "directory", makeAdapter: func(t *testing.T, path string) { t.Helper(); mustMkdir(t, path) }},
		{name: "not executable", makeAdapter: func(t *testing.T, path string) { t.Helper(); mustWrite(t, path, []byte("not executable"), 0o600) }},
		{name: "shell symlink", makeAdapter: func(t *testing.T, path string) {
			t.Helper()
			if runtime.GOOS == "windows" {
				t.Skip("symlink shell fixture is Unix-specific")
			}
			if err := os.Symlink("/bin/sh", path); err != nil {
				t.Fatalf("Symlink(): %v", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installDir := t.TempDir()
			briaExecutable := executableFixture(t, installDir, "bria", false)
			rawCodex := executableFixture(t, installDir, "raw-codex", false)
			adapterPath := filepath.Join(installDir, executableName("bria-codex-adapter"))
			if test.makeAdapter != nil {
				test.makeAdapter(t, adapterPath)
			}
			configuration := validConfig(t, installDir)
			configuration.Providers["codex"] = config.ProviderConfig{
				Enabled: true,
				Command: &config.ProviderCommand{Exec: rawCodex, Argv: []string{"app-server"}},
			}
			configuration.Providers["claude"] = config.ProviderConfig{Enabled: false}
			secret := "never-print-this-value"
			parent := testEnvironment("RUNTIMEFACTORY_TELEGRAM_TOKEN=" + secret)
			_, err := runtimefactory.NewStarter(configuration, parent, briaExecutable, sessionruntime.Options{})
			if err == nil {
				t.Fatal("NewStarter() error = nil")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), adapterPath) {
				t.Fatalf("NewStarter() error leaks secret or path: %v", err)
			}
		})
	}
}

func runAdapterHelper() {
	expectedArg := os.Getenv("RUNTIMEFACTORY_EXPECT_ARG")
	if expectedArg == "" {
		expectedArg = "app-server"
	}
	expectedAdapter, err := filepath.EvalSymlinks(os.Getenv("RUNTIMEFACTORY_EXPECT_ADAPTER"))
	if err != nil || os.Args[0] != expectedAdapter ||
		!reflect.DeepEqual(os.Args[1:], []string{"--", os.Getenv("RUNTIMEFACTORY_EXPECT_RAW"), expectedArg}) ||
		os.Getenv("RUNTIMEFACTORY_SAFE") != "preserved" || os.Getenv("BRIA_PARENT_SECRET") != "" {
		os.Exit(70)
	}
	if os.Getenv("RUNTIMEFACTORY_EXPECT_TOKEN_PRESERVED") == "1" {
		if os.Getenv("RUNTIMEFACTORY_TELEGRAM_TOKEN") != "unrelated-value" {
			os.Exit(71)
		}
	} else if os.Getenv("RUNTIMEFACTORY_TELEGRAM_TOKEN") != "" {
		os.Exit(72)
	}
	if os.Getenv("BRIA_PROVIDER") != os.Getenv("RUNTIMEFACTORY_EXPECT_PROVIDER") || os.Getenv("BRIA_SESSION_ID") == "" {
		os.Exit(73)
	}
	if expected := os.Getenv("RUNTIMEFACTORY_EXPECT_CLAUDE_CREDENTIAL"); expected != "" && os.Getenv("BRIA_PROVIDER_CREDENTIAL_FILE") != expected {
		os.Exit(76)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"protocol": 1, "type": "ready", "provider_session_id": "factory-provider-session",
		"readiness": "protocol", "authentication": "unknown",
	})
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(74)
	}
	var request map[string]any
	if json.Unmarshal(scanner.Bytes(), &request) != nil || request["type"] != "close" {
		os.Exit(75)
	}
}

func validConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	return config.Config{
		OwnerUserID: 1, PrivateChatID: 2, BotUsername: "bria_test_bot",
		StatePath:     filepath.Join(dir, "state.json"),
		TelegramToken: config.TelegramTokenRef{EnvVar: "RUNTIMEFACTORY_TELEGRAM_TOKEN"},
		CallbackKey:   config.CallbackKeyRef{SecretFile: filepath.Join(dir, "callback-key")},
		Providers: map[string]config.ProviderConfig{
			"codex":  {Enabled: false},
			"claude": {Enabled: false},
		},
	}
}

func executableFixture(t *testing.T, dir string, name string, copySelf bool) string {
	t.Helper()
	path := filepath.Join(dir, executableName(name))
	content := []byte("fixture")
	if copySelf {
		var err error
		content, err = os.ReadFile(os.Args[0])
		if err != nil {
			t.Fatalf("ReadFile(test binary): %v", err)
		}
	}
	mustWrite(t, path, content, 0o700)
	return path
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func mustWrite(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir(): %v", err)
	}
}

func testEnvironment(extra ...string) []string {
	blocked := make(map[string]bool)
	for _, entry := range extra {
		name, _, _ := strings.Cut(entry, "=")
		blocked[name] = true
	}
	result := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[name] && !strings.HasPrefix(name, "BRIA_") {
			result = append(result, entry)
		}
	}
	return append(result, extra...)
}
