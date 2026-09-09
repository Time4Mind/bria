// Package runtimecommand validates immutable adapter executable and environment identity.
package runtimecommand

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Spec struct {
	PersistentTerminal     bool
	Path                   string
	Args                   []string
	Env                    []string
	ProviderCredentialFile string
}

type Verified struct {
	Spec     Spec
	Identity os.FileInfo
}

func Verify(command Spec) (Verified, error) {
	if strings.TrimSpace(command.Path) == "" || !filepath.IsAbs(command.Path) {
		return Verified{}, errors.New("executable path must be absolute")
	}
	if strings.ContainsRune(command.Path, '\x00') {
		return Verified{}, errors.New("executable path contains NUL")
	}
	if isShellExecutable(command.Path) {
		return Verified{}, errors.New("executable must not be a shell or command-discovery launcher")
	}
	resolved, err := filepath.EvalSymlinks(command.Path)
	if err != nil {
		return Verified{}, errors.New("executable target must exist")
	}
	if isShellExecutable(resolved) {
		return Verified{}, errors.New("resolved executable must not be a shell or command-discovery launcher")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return Verified{}, errors.New("executable target must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return Verified{}, errors.New("executable target must be executable")
	}
	for _, argument := range command.Args {
		if strings.ContainsRune(argument, '\x00') {
			return Verified{}, errors.New("command argument contains NUL")
		}
	}
	if command.ProviderCredentialFile != "" {
		if !filepath.IsAbs(command.ProviderCredentialFile) || strings.ContainsRune(command.ProviderCredentialFile, '\x00') {
			return Verified{}, errors.New("provider credential file reference must be an absolute path")
		}
		command.ProviderCredentialFile = filepath.Clean(command.ProviderCredentialFile)
	}
	seen := make(map[string]bool, len(command.Env))
	for _, entry := range command.Env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !validEnvironmentName(name) || strings.ContainsRune(value, '\x00') {
			return Verified{}, errors.New("environment entry has invalid KEY=VALUE syntax")
		}
		if name == "BRIA_SESSION_ID" || name == "BRIA_PROVIDER" ||
			name == "BRIA_START_MODE" || name == "BRIA_PROVIDER_SESSION_ID" || name == "BRIA_GENERATION" ||
			name == "BRIA_PROVIDER_CREDENTIAL_FILE" || name == "BRIA_ATTACH_ONLY" || name == "BRIA_PERSISTENT_TERMINAL" {
			return Verified{}, errors.New("environment must not override reserved Bria variables")
		}
		if seen[name] {
			return Verified{}, errors.New("environment contains duplicate keys")
		}
		seen[name] = true
	}
	return Verified{
		Spec: Spec{
			PersistentTerminal:     command.PersistentTerminal,
			Path:                   resolved,
			Args:                   append([]string(nil), command.Args...),
			Env:                    append([]string(nil), command.Env...),
			ProviderCredentialFile: command.ProviderCredentialFile,
		},
		Identity: info,
	}, nil
}

func VerifyIdentity(path string, identity os.FileInfo) error {
	current, err := os.Stat(path)
	if err != nil || !current.Mode().IsRegular() {
		return errors.New("verified executable is unavailable")
	}
	if runtime.GOOS != "windows" && current.Mode().Perm()&0o111 == 0 {
		return errors.New("verified executable is no longer executable")
	}
	if !os.SameFile(identity, current) {
		return errors.New("verified executable identity changed")
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index := range len(name) {
		character := name[index]
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func isShellExecutable(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "csh", "tcsh",
		"cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe",
		"env", "env.exe":
		return true
	default:
		return false
	}
}
