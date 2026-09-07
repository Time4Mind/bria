// Package nativecli plans native CLI launches and verifies startup identity.
package nativecli

import (
	"bria/internal/domain"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type Plan struct {
	Workdir   string
	Command   []string
	SessionID string
	Provider  domain.Provider
}

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func Build(provider domain.Provider, rawCommand []string, workdir, resumeID string) (Plan, error) {
	if len(rawCommand) == 0 || strings.TrimSpace(rawCommand[0]) == "" || !filepath.IsAbs(workdir) {
		return Plan{}, errors.New("native CLI requires executable and absolute workdir")
	}
	if provider != domain.ProviderCodex && provider != domain.ProviderClaude {
		return Plan{}, errors.New("unsupported native CLI provider")
	}
	if resumeID != "" && !uuidPattern.MatchString(resumeID) {
		return Plan{}, errors.New("native CLI resume requires exact UUID")
	}
	command := []string{rawCommand[0]}
	// Strip only known transport and session-selection options. Preserve known
	// configuration options with their operands; reject positional prompts or
	// unknown flags rather than accidentally starting a different CLI operation.
	values := map[string]bool{"--model": true, "--effort": true, "--settings": true, "--setting-sources": true, "--append-system-prompt": true, "--system-prompt": true, "--add-dir": true, "--mcp-config": true, "--plugin-dir": true, "--agent": true, "--agents": true, "--config": true, "--profile": true, "--enable": true, "--disable": true, "--local-provider": true, "-m": true}
	flags := map[string]bool{"--verbose": true, "--strict-mcp-config": true, "--no-chrome": true, "--search": true, "--oss": true, "--strict-config": true}
	stripValues := map[string]bool{"--input-format": true, "--output-format": true, "--permission-mode": true, "--sandbox": true, "-s": true, "--ask-for-approval": true, "-a": true, "--cd": true, "-C": true, "--session-id": true, "--resume": true, "-r": true}
	stripFlags := map[string]bool{"--stdio": true, "--print": true, "--include-partial-messages": true, "--replay-user-messages": true, "--dangerously-skip-permissions": true, "--allow-dangerously-skip-permissions": true, "--dangerously-bypass-approvals-and-sandbox": true, "--no-alt-screen": true, "--continue": true, "--full-auto": true}
	if provider == domain.ProviderCodex {
		values["-c"], values["-p"] = true, true
	} else {
		stripFlags["-c"], stripFlags["-p"] = true, true
	}
	for i := 1; i < len(rawCommand); i++ {
		arg := rawCommand[i]
		if provider == domain.ProviderCodex && arg == "app-server" {
			continue
		}
		key, _, equals := strings.Cut(arg, "=")
		if stripFlags[key] {
			continue
		}
		if stripValues[key] || values[key] {
			if !equals {
				if i+1 >= len(rawCommand) {
					return Plan{}, fmt.Errorf("native CLI option %s requires a value", key)
				}
				i++
			}
			if values[key] {
				command = append(command, arg)
				if !equals {
					command = append(command, rawCommand[i])
				}
			}
			continue
		}
		if flags[key] && !equals {
			command = append(command, arg)
			continue
		}
		return Plan{}, errors.New("unsupported configured native CLI argument")
	}
	plan := Plan{Provider: provider, SessionID: strings.ToLower(resumeID), Workdir: filepath.Clean(workdir)}
	if provider == domain.ProviderCodex {
		command = append(command, "--dangerously-bypass-approvals-and-sandbox", "--no-alt-screen", "--cd", workdir)
		if resumeID != "" {
			command = append(command, "resume", resumeID)
		}
	} else {
		command = append(command, "--dangerously-skip-permissions")
		if resumeID != "" {
			command = append(command, "--resume", resumeID)
		} else {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return Plan{}, err
			}
			id[6] = (id[6] & 15) | 64
			id[8] = (id[8] & 63) | 128
			plan.SessionID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
			command = append(command, "--session-id", plan.SessionID)
		}
	}
	plan.Command = command
	return plan, nil
}
