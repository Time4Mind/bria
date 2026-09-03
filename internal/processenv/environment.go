// Package processenv prepares the inherited environment of local provider
// processes.
package processenv

import (
	"fmt"
	"runtime"
	"strings"
)

// Options defines environment names and target platform semantics. GOOS
// defaults to runtime.GOOS; setting it is useful when verifying another
// platform's key-comparison behavior.
type Options struct {
	// TelegramTokenEnv is the exact environment key holding the Telegram token.
	// An empty value means the token does not come from the environment.
	TelegramTokenEnv string
	GOOS             string
}

// Build returns the environment safe to pass to a local provider process. It
// rejects ambiguous input before removing reserved Bria and Telegram entries.
func Build(parent []string, options Options) ([]string, error) {
	goos := options.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	canonicalKey := func(key string) string {
		if goos == "windows" {
			return strings.ToUpper(key)
		}
		return key
	}

	if options.TelegramTokenEnv != "" {
		if err := validateKey(options.TelegramTokenEnv); err != nil {
			return nil, fmt.Errorf("Telegram token environment key is malformed: %w", err)
		}
	}

	type entry struct {
		raw       string
		canonical string
	}
	entries := make([]entry, 0, len(parent))
	seen := make(map[string]struct{}, len(parent))
	for index, raw := range parent {
		key, err := parseKey(raw)
		if err != nil {
			return nil, fmt.Errorf("environment entry %d is malformed: %w", index, err)
		}
		canonical := canonicalKey(key)
		if _, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("duplicate environment key %q", key)
		}
		seen[canonical] = struct{}{}
		entries = append(entries, entry{raw: raw, canonical: canonical})
	}

	telegramKey := canonicalKey(options.TelegramTokenEnv)
	briaPrefix := canonicalKey("BRIA_")
	homeKey := canonicalKey("HOME")
	pathKey := canonicalKey("PATH")
	hasHome := false
	hasPath := false
	result := make([]string, 0, len(entries))
	for _, candidate := range entries {
		if strings.HasPrefix(candidate.canonical, briaPrefix) || (options.TelegramTokenEnv != "" && candidate.canonical == telegramKey) {
			continue
		}
		switch candidate.canonical {
		case homeKey:
			hasHome = true
		case pathKey:
			hasPath = true
		}
		result = append(result, candidate.raw)
	}
	if !hasHome {
		return nil, fmt.Errorf("required environment key %q is absent", "HOME")
	}
	if !hasPath {
		return nil, fmt.Errorf("required environment key %q is absent", "PATH")
	}
	return result, nil
}

func parseKey(entry string) (string, error) {
	delimiter := strings.IndexByte(entry, '=')
	if delimiter < 0 {
		return "", fmt.Errorf("missing '=' delimiter")
	}
	key := entry[:delimiter]
	if err := validateKey(key); err != nil {
		return "", err
	}
	if strings.IndexByte(entry[delimiter+1:], 0) >= 0 {
		return "", fmt.Errorf("value for key %q contains NUL", key)
	}
	return key, nil
}

func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("environment key is empty")
	}
	if strings.ContainsAny(key, "=\x00") {
		return fmt.Errorf("environment key is invalid")
	}
	return nil
}
