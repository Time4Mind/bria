package providerauth

import (
	"os"
	"strings"
)

var sensitiveEnvironment = map[string]bool{
	"ANTHROPIC_API_KEY": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
	"OPENAI_API_KEY": true, "CODEX_API_KEY": true,
}

func authenticationEnvironment() []string {
	result := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "TMUX" || sensitiveEnvironment[name] {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "IS_SANDBOX=1")
}
