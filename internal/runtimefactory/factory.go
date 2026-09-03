// Package runtimefactory composes configured raw providers with Bria's local
// provider adapters.
package runtimefactory

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/processenv"
	"bria/internal/sessionruntime"
)

var (
	ErrConfiguration      = errors.New("invalid runtime factory configuration")
	ErrEnvironment        = errors.New("invalid provider process environment")
	ErrExecutable         = errors.New("runtime executable is unavailable or unsafe")
	ErrStarterComposition = errors.New("cannot compose session runtime")
)

// CommandSet is the immutable set of exact local adapter commands derived
// from one validated configuration snapshot.
type CommandSet struct {
	commands map[domain.Provider]sessionruntime.CommandSpec
}

// NewCommandSet validates and snapshots enabled provider adapter commands.
// Returned specs contain only the stripped child environment and are always
// copied before leaving the set.
func NewCommandSet(configuration config.Config, parentEnvironment []string, briaExecutable string) (*CommandSet, error) {
	return newCommandSet(configuration, parentEnvironment, briaExecutable, false)
}

// NewConfiguredCommandSet snapshots every configured provider command,
// including currently disabled providers, without enabling them.
func NewConfiguredCommandSet(configuration config.Config, parentEnvironment []string, briaExecutable string) (*CommandSet, error) {
	return newCommandSet(configuration, parentEnvironment, briaExecutable, true)
}

func newCommandSet(configuration config.Config, parentEnvironment []string, briaExecutable string, includeDisabled bool) (*CommandSet, error) {
	if err := configuration.Validate(); err != nil {
		return nil, ErrConfiguration
	}
	if err := validateExecutable(briaExecutable); err != nil {
		return nil, ErrExecutable
	}
	safeEnvironment, err := processenv.Build(parentEnvironment, processenv.Options{
		TelegramTokenEnv: configuration.TelegramToken.EnvVar,
	})
	if err != nil {
		return nil, ErrEnvironment
	}
	commands := make(map[domain.Provider]sessionruntime.CommandSpec, 2)
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		rawCommand, enabled := configuration.EnabledCommand(provider)
		if !enabled {
			configured := configuration.Providers[string(provider)]
			if !includeDisabled || configured.Command == nil {
				continue
			}
			rawCommand = *configured.Command
			rawCommand.Argv = append([]string(nil), configured.Command.Argv...)
		}
		adapterPath := siblingAdapterPath(briaExecutable, provider)
		if err := validateExecutable(adapterPath); err != nil || validateExecutable(rawCommand.Exec) != nil {
			return nil, ErrExecutable
		}
		spec := sessionruntime.CommandSpec{
			Path: adapterPath,
			Args: append([]string{"--", rawCommand.Exec}, rawCommand.Argv...),
			Env:  append([]string(nil), safeEnvironment...),
		}
		if provider == domain.ProviderClaude {
			spec.ProviderCredentialFile = configuration.StatePath + ".claude-api-key.json"
		}
		commands[provider] = spec
	}
	return &CommandSet{commands: commands}, nil
}

// CommandSpec returns an independent copy of the exact configured adapter
// command for provider. A disabled or unknown provider returns false.
func (set *CommandSet) CommandSpec(provider domain.Provider) (sessionruntime.CommandSpec, bool) {
	if set == nil {
		return sessionruntime.CommandSpec{}, false
	}
	spec, ok := set.commands[provider]
	if !ok {
		return sessionruntime.CommandSpec{}, false
	}
	return cloneCommandSpec(spec), true
}

// NewStarter constructs a runtime from the same immutable command snapshot
// exposed to recovery composition.
func (set *CommandSet) NewStarter(options sessionruntime.Options) (*sessionruntime.Starter, error) {
	if set == nil {
		return nil, ErrStarterComposition
	}
	starter, err := sessionruntime.NewStarter(set.cloneCommands(), options)
	if err != nil {
		return nil, ErrStarterComposition
	}
	return starter, nil
}

// NewStarter constructs the provider-neutral runtime used by command
// composition. parentEnvironment is intended to be the caller's os.Environ()
// value; accepting it explicitly keeps secret stripping independently
// testable. briaExecutable must be the exact absolute current Bria executable,
// not a name discovered through PATH.
func NewStarter(
	configuration config.Config,
	parentEnvironment []string,
	briaExecutable string,
	options sessionruntime.Options,
) (*sessionruntime.Starter, error) {
	commandSet, err := NewCommandSet(configuration, parentEnvironment, briaExecutable)
	if err != nil {
		return nil, err
	}
	return commandSet.NewStarter(options)
}

func (set *CommandSet) cloneCommands() map[domain.Provider]sessionruntime.CommandSpec {
	commands := make(map[domain.Provider]sessionruntime.CommandSpec, len(set.commands))
	for provider, spec := range set.commands {
		commands[provider] = cloneCommandSpec(spec)
	}
	return commands
}

func cloneCommandSpec(spec sessionruntime.CommandSpec) sessionruntime.CommandSpec {
	spec.Args = append([]string(nil), spec.Args...)
	spec.Env = append([]string(nil), spec.Env...)
	return spec
}

func siblingAdapterPath(briaExecutable string, provider domain.Provider) string {
	extension := ""
	if strings.EqualFold(filepath.Ext(briaExecutable), ".exe") {
		extension = ".exe"
	}
	return filepath.Join(filepath.Dir(briaExecutable), "bria-"+string(provider)+"-adapter"+extension)
}

func validateExecutable(path string) error {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return ErrExecutable
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || isShellOrDiscovery(resolved) {
		return ErrExecutable
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return ErrExecutable
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return ErrExecutable
	}
	return nil
}

func isShellOrDiscovery(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "csh", "tcsh",
		"cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe",
		"env", "env.exe":
		return true
	default:
		return false
	}
}
