package telegramsettings_test

import (
	"strings"
	"testing"

	"bria/internal/telegramsettings"
)

func TestNormalizeNodeName(t *testing.T) {
	if got, err := telegramsettings.NormalizeNodeName("  Рабочая нода  "); err != nil || got != "Рабочая нода" {
		t.Fatalf("NormalizeNodeName(valid) = %q, %v", got, err)
	}
	for _, invalid := range []string{"", " \t ", "line one\nline two", strings.Repeat("я", 65), strings.Repeat("🙂", 33)} {
		if got, err := telegramsettings.NormalizeNodeName(invalid); err == nil || got != "" {
			t.Fatalf("NormalizeNodeName(%q) = %q, %v; want rejection", invalid, got, err)
		}
	}
}
