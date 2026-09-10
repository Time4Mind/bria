package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestClassifyCIScopeAllowsOnlyReleaseJournalMarkdown(t *testing.T) {
	t.Parallel()

	for _, paths := range [][]string{
		{"docs/CI_ACCELERATION_TODO.md"},
		{"docs/STATUS_AND_NEXT.md"},
		{"docs/STATUS_AND_NEXT.md", "docs/CI_ACCELERATION_TODO.md"},
	} {
		if got := classifyCIScope(paths); got != ciScopeDocsOnly {
			t.Fatalf("classifyCIScope(%q) = %q, want %q", paths, got, ciScopeDocsOnly)
		}
	}
}

func TestRunCIScopeReadsNULTerminatedGitPaths(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := runCIScope(strings.NewReader("docs/ONE_TODO.md\x00docs/TWO_TODO.md\x00"), &output); err != nil {
		t.Fatalf("runCIScope() error = %v", err)
	}
	if got, want := output.String(), ciScopeDocsOnly+"\n"; got != want {
		t.Fatalf("runCIScope() output = %q, want %q", got, want)
	}
}

func TestRunCIScopeFailsClosedForNonNULTerminatedInput(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := runCIScope(strings.NewReader("docs/ONE_TODO.md"), &output); err != nil {
		t.Fatalf("runCIScope() error = %v", err)
	}
	if got, want := output.String(), ciScopeFull+"\n"; got != want {
		t.Fatalf("runCIScope() output = %q, want %q", got, want)
	}
}

func TestClassifyCIScopeFailsClosedForRuntimeSensitivePaths(t *testing.T) {
	t.Parallel()

	for name, paths := range map[string][]string{
		"empty diff":        nil,
		"root markdown":     {"README.md"},
		"policy":            {"AGENTS.md"},
		"workflow":          {".github/workflows/context.yml"},
		"makefile":          {"Makefile"},
		"go source":         {"internal/app/service.go"},
		"non-markdown docs": {"docs/generator.sh"},
		"harness":           {"docs/HARNESS.md"},
		"product docs":      {"docs/PRODUCT.md"},
		"nested todo":       {"docs/archive/OLD_TODO.md"},
		"mixed":             {"docs/CI_ACCELERATION_TODO.md", "go.mod"},
		"path traversal":    {"docs/../AGENTS.md"},
	} {
		name, paths := name, paths
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := classifyCIScope(paths); got != ciScopeFull {
				t.Fatalf("classifyCIScope(%q) = %q, want %q", paths, got, ciScopeFull)
			}
		})
	}
}
