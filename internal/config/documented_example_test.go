package config_test

import (
	"os"
	"strings"
	"testing"

	"bria/internal/config"
)

func TestDocumentedSingleMachineConfigurationDecodesWithoutSecrets(t *testing.T) {
	body, err := os.ReadFile("../../docs/examples/bria-single-machine.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Decode(strings.NewReader(string(body)))
	if err != nil || c.EffectiveRole() != config.RoleCombined {
		t.Fatalf("documented example invalid: %v", err)
	}
	if c.Providers["codex"].Enabled || c.Providers["claude"].Enabled {
		t.Fatal("portable example must not start providers")
	}
	for _, field := range []string{`"launch_mode":"ccbot",`, `"auto_approve_commands":false,`} {
		invalid := strings.Replace(string(body), "{", "{"+field, 1)
		if _, err := config.Decode(strings.NewReader(invalid)); err == nil {
			t.Fatal("undocumented config field accepted", field)
		}
	}
}
