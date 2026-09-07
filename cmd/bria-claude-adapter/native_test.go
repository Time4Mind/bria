package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/provider/claude"
)

func TestNativeStoredCredentialOnlyEntersChildEnvironment(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "credential.json")
	const fakeKey = "sk-ant-native-fixture-only"
	data, err := json.Marshal(map[string]any{"version": 1, "operation_id": "fixture", "computer_id": "local", "api_key": []byte(fakeKey)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential, data, 0600); err != nil {
		t.Fatal(err)
	}
	parent := []string{"BRIA_START_MODE=new", claude.CredentialFileEnvironment + "=" + credential, "ANTHROPIC_API_KEY=old-fixture-key", "HOME=" + dir}
	original := append([]string(nil), parent...)
	configuration, err := nativeConfiguration([]string{"--", os.Args[0]}, dir, parent)
	if err != nil {
		t.Fatal("native configuration rejected valid synthetic credential")
	}
	count := 0
	for _, entry := range configuration.Environment {
		if strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") {
			count++
			if entry != "ANTHROPIC_API_KEY="+fakeKey {
				t.Fatal("child credential differs from private record")
			}
		}
	}
	if count != 1 || !reflect.DeepEqual(parent, original) {
		t.Fatal("credential injection duplicated key or mutated parent")
	}
}

func TestNativeMissingCredentialPreservesOAuthAndInvalidFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "missing.json")
	parent := []string{"BRIA_START_MODE=new", claude.CredentialFileEnvironment + "=" + credential, "CLAUDE_CODE_OAUTH_TOKEN=synthetic-oauth"}
	configuration, err := nativeConfiguration([]string{"--", os.Args[0]}, dir, parent)
	if err != nil || !reflect.DeepEqual(configuration.Environment, parent) {
		t.Fatal("absent key prevented normal OAuth environment")
	}
	if err := os.WriteFile(credential, []byte("invalid fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeConfiguration([]string{"--", os.Args[0]}, dir, parent); err == nil {
		t.Fatal("invalid existing credential silently fell back")
	}
}
