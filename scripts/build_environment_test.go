package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRecipesHonorExplicitModuleCacheAndRaceExitPolicy(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "module-cache")
	cmd := exec.Command("make", "-n", "check-test", "check-race", "GOMODCACHE="+cache)
	cmd.Dir = ".."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect check recipes: %v: %s", err, output)
	}
	if strings.Count(string(output), cache) != 2 || !strings.Contains(string(output), "GOPROXY=off") || !strings.Contains(string(output), "GORACE=atexit_sleep_ms=0") {
		t.Fatalf("checks must share explicit cache, remain offline and avoid child race-runtime delay: %s", output)
	}
}

func TestDependencyPreparationUsesSameCacheAndChecksumVerification(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "module-cache")
	cmd := exec.Command("make", "-n", "prepare-deps", "GOMODCACHE="+cache)
	cmd.Dir = ".."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect dependency preparation: %v: %s", err, output)
	}
	for _, required := range []string{cache, "GOSUMDB=sum.golang.org", "mod download", "mod verify"} {
		if !strings.Contains(string(output), required) {
			t.Fatalf("dependency preparation missing %q: %s", required, output)
		}
	}
}
