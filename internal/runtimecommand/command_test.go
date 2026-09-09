package runtimecommand_test

import (
	"os"
	"testing"

	"bria/internal/runtimecommand"
)

func TestVerifiedPersistentCommandOwnsEnvironmentAndRejectsIdentityOverrides(t *testing.T) {
	for _, name := range []string{"BRIA_ATTACH_ONLY", "BRIA_PERSISTENT_TERMINAL", "BRIA_SESSION_ID", "BRIA_PROVIDER_SESSION_ID", "BRIA_GENERATION"} {
		if _, err := runtimecommand.Verify(runtimecommand.Spec{Path: os.Args[0], Env: []string{name + "=1"}}); err == nil {
			t.Fatalf("reserved override %s", name)
		}
	}
	spec := runtimecommand.Spec{Path: os.Args[0], PersistentTerminal: true, Args: []string{"--native"}, Env: []string{"SAFE=one"}}
	verified, err := runtimecommand.Verify(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Args[0], spec.Env[0] = "changed", "SAFE=two"
	if !verified.Spec.PersistentTerminal || verified.Spec.Args[0] != "--native" || verified.Spec.Env[0] != "SAFE=one" {
		t.Fatal("mutable verified command")
	}
	if err := runtimecommand.VerifyIdentity(verified.Spec.Path, verified.Identity); err != nil {
		t.Fatal(err)
	}
}
