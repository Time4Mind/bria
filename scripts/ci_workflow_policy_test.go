package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const actionsCacheCommit = "actions/cache@0057852bfaa89a56745cba8c7296529d2fc39830"

func TestMakefileExposesCompleteNonRaceReleaseGate(t *testing.T) {
	t.Parallel()

	makefile := readMakefile(t)
	for _, required := range []string{
		"check-executable: build",
		"check-full-no-race: check check-executable",
		"check-full: check check-race check-executable",
	} {
		if !strings.Contains(makefile, required) {
			t.Fatalf("Makefile is missing %q", required)
		}
	}
}

func TestStageOneWorkflowRoutesDocsAndParallelizesFullChecks(t *testing.T) {
	t.Parallel()

	workflow := readRepositoryFile(t, ".github/workflows/context.yml")
	for _, required := range []string{
		"go run -mod=readonly ./scripts ci-scope",
		"classify:",
		"Run documentation gate",
		"stage1_standard:",
		"stage1_race:",
		"validate_stage_1:",
		"name: validate-stage-1",
		"make check-full-no-race",
		"make check-race",
		"make check-policy",
		"git diff --no-renames --name-only -z",
		"CLASSIFY_RESULT",
		"STANDARD_RESULT",
		"RACE_RESULT",
		actionsCacheCommit,
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("Stage 1 workflow is missing %q", required)
		}
	}
	if got := strings.Count(workflow, "if: needs.classify.outputs.scope != 'docs-only'"); got != 2 {
		t.Fatalf("Stage 1 workflow has %d full-gate conditions, want 2", got)
	}
}

func TestPlatformWorkflowUsesTheSameFailClosedScopeAndCachesGo(t *testing.T) {
	t.Parallel()

	workflow := readRepositoryFile(t, ".github/workflows/platform.yml")
	for _, required := range []string{
		"go run -mod=readonly ./scripts ci-scope",
		"needs: classify",
		"if: needs.classify.outputs.scope != 'docs-only'",
		"validate_platform:",
		"name: validate-platform",
		"git diff --no-renames --name-only -z",
		"CLASSIFY_RESULT",
		"NATIVE_RESULT",
		"CROSS_RESULT",
		"WSL_RESULT",
		"DOCKER_RESULT",
		actionsCacheCommit,
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("Platform workflow is missing %q", required)
		}
	}
	if got := strings.Count(workflow, "if: needs.classify.outputs.scope != 'docs-only'"); got != 4 {
		t.Fatalf("Platform workflow has %d full-gate conditions, want 4", got)
	}
}

func readRepositoryFile(t *testing.T, relativePath string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return string(data)
}
