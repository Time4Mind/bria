package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

type testLayout struct {
	claude string
	codex  string
}

func newTestLayout(t *testing.T) testLayout {
	t.Helper()
	root := t.TempDir()
	layout := testLayout{
		claude: filepath.Join(root, "claude-projects"),
		codex:  filepath.Join(root, "codex-sessions"),
	}
	for _, directory := range []string{layout.claude, layout.codex} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return layout
}

func newTestReader(t *testing.T, layout testLayout, mutate func(*Config)) *Reader {
	t.Helper()
	config := Config{
		ClaudeProjectsRoot: layout.claude,
		CodexSessionsRoot:  layout.codex,
	}
	if mutate != nil {
		mutate(&config)
	}
	reader, err := NewReader(config)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
