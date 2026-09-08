package nativeapproval

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Fixtures are public synthetic copies, not a vehicle for publishing internal
// work-source identities. Raw receipts remain in the owner's ignored evidence.
func TestPublicCorpusUsesSyntheticSourceIdentities(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.txt")
	if err != nil || len(paths) == 0 {
		t.Fatal("approval corpus missing", err)
	}
	hosts := regexp.MustCompile(`https?://([^/"\s]+)`)
	tickets := regexp.MustCompile(`get_issue\("([A-Z]+-[0-9]+)"\)`)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range hosts.FindAllSubmatch(data, -1) {
			if !strings.HasSuffix(string(match[1]), ".invalid") {
				t.Fatalf("%s contains a non-synthetic source host", path)
			}
		}
		for _, match := range tickets.FindAllSubmatch(data, -1) {
			if !strings.HasPrefix(string(match[1]), "EXAMPLEPRJX-") {
				t.Fatalf("%s contains a non-synthetic issue identity", path)
			}
		}
	}
}
