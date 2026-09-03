package update_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"bria/internal/update"
)

func TestVerifyReleaseRejectsInvalidSignaturePlatformAndHash(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(): %v", err)
	}
	payload := []byte("release-binary")
	digest := sha256.Sum256(payload)
	manifest := update.Manifest{
		FormatVersion: update.ManifestFormatVersion,
		Version:       "1.2.3",
		Artifacts: []update.Artifact{{
			Name: "bria-darwin-arm64", Platform: "darwin", Arch: "arm64",
			Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
		}},
	}
	manifestBytes, err := update.MarshalManifest(manifest)
	if err != nil {
		t.Fatalf("MarshalManifest(): %v", err)
	}
	signature := ed25519.Sign(privateKey, manifestBytes)

	if _, err := update.VerifyManifest(manifestBytes, append([]byte(nil), signature[:len(signature)-1]...), publicKey); !errors.Is(err, update.ErrInvalidSignature) {
		t.Fatalf("invalid signature error = %v, want ErrInvalidSignature", err)
	}
	verified, err := update.VerifyManifest(manifestBytes, signature, publicKey)
	if err != nil {
		t.Fatalf("VerifyManifest(): %v", err)
	}
	if _, err := update.SelectArtifact(verified, "linux", "arm64"); !errors.Is(err, update.ErrNoArtifact) {
		t.Fatalf("wrong platform error = %v, want ErrNoArtifact", err)
	}
	artifact, err := update.SelectArtifact(verified, "darwin", "arm64")
	if err != nil {
		t.Fatalf("SelectArtifact(): %v", err)
	}
	if err := update.VerifyArtifact(bytes.NewReader([]byte("tampered")), artifact); !errors.Is(err, update.ErrArtifactHash) {
		t.Fatalf("tampered artifact error = %v, want ErrArtifactHash", err)
	}
	if err := update.VerifyArtifact(bytes.NewReader(payload), artifact); err != nil {
		t.Fatalf("VerifyArtifact(good): %v", err)
	}
}

func TestBuildManifestHashesVersionedArtifactsAndCanonicalizesOrder(t *testing.T) {
	t.Parallel()

	manifest, err := update.BuildManifest("2.4.0", []update.ArtifactSource{
		{Name: "bria-linux-amd64", Platform: "linux", Arch: "amd64", Content: strings.NewReader("linux")},
		{Name: "bria-darwin-arm64", Platform: "darwin", Arch: "arm64", Content: strings.NewReader("darwin")},
	})
	if err != nil {
		t.Fatalf("BuildManifest(): %v", err)
	}
	if manifest.FormatVersion != update.ManifestFormatVersion || manifest.Version != "2.4.0" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if got, want := []string{manifest.Artifacts[0].Name, manifest.Artifacts[1].Name}, []string{"bria-darwin-arm64", "bria-linux-amd64"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact order = %#v, want %#v", got, want)
	}
	linuxDigest := sha256.Sum256([]byte("linux"))
	if got, want := manifest.Artifacts[1], (update.Artifact{
		Name: "bria-linux-amd64", Platform: "linux", Arch: "amd64", Size: 5,
		SHA256: hex.EncodeToString(linuxDigest[:]),
	}); got != want {
		t.Fatalf("linux artifact = %#v, want %#v", got, want)
	}
	if _, err := update.MarshalManifest(manifest); err != nil {
		t.Fatalf("built manifest is not canonical: %v", err)
	}
}

func TestSignedManifestRequiresTrustedKeyReferenceAndBindsKeyID(t *testing.T) {
	t.Parallel()

	primaryPublic, primaryPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(primary): %v", err)
	}
	manifest, err := update.BuildManifest("2.4.0", []update.ArtifactSource{{
		Name: "bria-linux-amd64", Platform: "linux", Arch: "amd64", Content: strings.NewReader("release"),
	}})
	if err != nil {
		t.Fatalf("BuildManifest(): %v", err)
	}
	signed, err := update.SignManifest(manifest, "primary", primaryPrivate)
	if err != nil {
		t.Fatalf("SignManifest(): %v", err)
	}
	verified, keyID, err := update.VerifySignedManifest(signed, update.TrustedKeys{"primary": primaryPublic})
	if err != nil {
		t.Fatalf("VerifySignedManifest(): %v", err)
	}
	if keyID != "primary" || !reflect.DeepEqual(verified, manifest) {
		t.Fatalf("verified manifest/key = %#v/%q", verified, keyID)
	}
	artifact, err := update.SelectArtifact(verified, "linux", "amd64")
	if err != nil {
		t.Fatalf("SelectArtifact(verified): %v", err)
	}
	if err := update.VerifyArtifact(strings.NewReader("release"), artifact); err != nil {
		t.Fatalf("VerifyArtifact(verified): %v", err)
	}
	if _, _, err := update.VerifySignedManifest(signed, update.TrustedKeys{}); !errors.Is(err, update.ErrUntrustedKey) {
		t.Fatalf("unknown key error = %v, want ErrUntrustedKey", err)
	}

	// Keep the same trusted public key under another same-length reference.
	// Verification must still fail because key_id is part of the signed bytes.
	substituted := bytes.Replace(signed, []byte(`"key_id":"primary"`), []byte(`"key_id":"backup1"`), 1)
	if bytes.Equal(substituted, signed) {
		t.Fatal("test did not substitute key id")
	}
	if _, _, err := update.VerifySignedManifest(substituted, update.TrustedKeys{
		"primary": primaryPublic,
		"backup1": primaryPublic,
	}); !errors.Is(err, update.ErrInvalidSignature) {
		t.Fatalf("substituted key error = %v, want ErrInvalidSignature", err)
	}
}

func TestRolloutOrdersExecutorsBeforeCoordinatorAndStopsWhenAvailabilityUnknown(t *testing.T) {
	t.Parallel()

	rollout, err := update.NewRollout("2.0.0", []update.Node{
		{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
		{ID: "executor-b", Role: update.RoleExecutor, CurrentVersion: "1.0.1", Availability: update.AvailabilityUnknown},
		{ID: "executor-a", Role: update.RoleExecutor, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
	})
	if err != nil {
		t.Fatalf("NewRollout(): %v", err)
	}
	if got, want := rollout.Order(), []string{"executor-b", "executor-a", "coordinator"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rollout order = %#v, want %#v", got, want)
	}
	if _, err := rollout.NextAction(); !errors.Is(err, update.ErrNodeUnavailable) {
		t.Fatalf("NextAction() error = %v, want ErrNodeUnavailable", err)
	}
	if rollout.Status() != update.RolloutStopped {
		t.Fatalf("rollout status = %q, want stopped", rollout.Status())
	}
	if rollout.Nodes()[0].State != update.NodeBlocked {
		t.Fatalf("unknown node state = %q, want blocked", rollout.Nodes()[0].State)
	}
}

func TestRolloutRequiresExactVersionHealthAndRollsBackBeforeStopping(t *testing.T) {
	t.Parallel()

	rollout, err := update.NewRollout("2.0.0", []update.Node{
		{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
		{ID: "executor", Role: update.RoleExecutor, CurrentVersion: "1.1.0", Availability: update.AvailabilityOnline},
	})
	if err != nil {
		t.Fatalf("NewRollout(): %v", err)
	}
	action, err := rollout.NextAction()
	if err != nil {
		t.Fatalf("NextAction(install executor): %v", err)
	}
	if action != (update.Action{Kind: update.ActionInstall, NodeID: "executor", Version: "2.0.0"}) {
		t.Fatalf("install action = %#v", action)
	}
	if err := rollout.RecordApplied("executor"); err != nil {
		t.Fatalf("RecordApplied(): %v", err)
	}
	wrongVersion := healthyReceipt("executor", "2.0.1")
	if err := rollout.RecordHealth(wrongVersion); !errors.Is(err, update.ErrUnhealthyReceipt) {
		t.Fatalf("wrong-version health error = %v, want ErrUnhealthyReceipt", err)
	}
	rollback, err := rollout.NextAction()
	if err != nil {
		t.Fatalf("NextAction(rollback): %v", err)
	}
	if rollback != (update.Action{Kind: update.ActionRollback, NodeID: "executor", Version: "1.1.0"}) {
		t.Fatalf("rollback action = %#v", rollback)
	}
	if err := rollout.RecordApplied("executor"); err != nil {
		t.Fatalf("RecordApplied(rollback): %v", err)
	}
	if err := rollout.RecordHealth(healthyReceipt("executor", "1.1.0")); err != nil {
		t.Fatalf("RecordHealth(rollback): %v", err)
	}
	if rollout.Status() != update.RolloutStopped {
		t.Fatalf("rollout status after rollback = %q, want stopped", rollout.Status())
	}
	nodes := rollout.Nodes()
	if nodes[0].State != update.NodeRolledBack || nodes[1].State != update.NodePending {
		t.Fatalf("node states after rollback = %#v", nodes)
	}
}

func TestSuccessfulRolloutUpdatesCoordinatorLast(t *testing.T) {
	t.Parallel()

	rollout, err := update.NewRollout("2.0.0", []update.Node{
		{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
		{ID: "executor", Role: update.RoleExecutor, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
	})
	if err != nil {
		t.Fatalf("NewRollout(): %v", err)
	}
	for _, id := range []string{"executor", "coordinator"} {
		action, err := rollout.NextAction()
		if err != nil {
			t.Fatalf("NextAction(%s): %v", id, err)
		}
		if action.NodeID != id || action.Kind != update.ActionInstall {
			t.Fatalf("action for %s = %#v", id, action)
		}
		if err := rollout.RecordApplied(id); err != nil {
			t.Fatalf("RecordApplied(%s): %v", id, err)
		}
		if err := rollout.RecordHealth(healthyReceipt(id, "2.0.0")); err != nil {
			t.Fatalf("RecordHealth(%s): %v", id, err)
		}
	}
	if rollout.Status() != update.RolloutCompleted {
		t.Fatalf("rollout status = %q, want completed", rollout.Status())
	}
}

func TestRolloutStateSurvivesPersistenceBetweenNodes(t *testing.T) {
	t.Parallel()

	rollout, err := update.NewRollout("2.0.0", []update.Node{
		{ID: "executor", Role: update.RoleExecutor, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
		{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
	})
	if err != nil {
		t.Fatalf("NewRollout(): %v", err)
	}
	if _, err := rollout.NextAction(); err != nil {
		t.Fatalf("NextAction(): %v", err)
	}
	if err := rollout.RecordApplied("executor"); err != nil {
		t.Fatalf("RecordApplied(): %v", err)
	}
	if err := rollout.RecordHealth(healthyReceipt("executor", "2.0.0")); err != nil {
		t.Fatalf("RecordHealth(): %v", err)
	}
	encoded, err := json.Marshal(rollout)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	var restored update.Rollout
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	action, err := restored.NextAction()
	if err != nil {
		t.Fatalf("restored NextAction(): %v", err)
	}
	if action.NodeID != "coordinator" {
		t.Fatalf("restored rollout action = %#v, want coordinator", action)
	}
}

func TestFailedRollbackEntersTerminalFailure(t *testing.T) {
	t.Parallel()

	rollout, err := update.NewRollout("2.0.0", []update.Node{
		{ID: "executor", Role: update.RoleExecutor, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
		{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
	})
	if err != nil {
		t.Fatalf("NewRollout(): %v", err)
	}
	if _, err := rollout.NextAction(); err != nil {
		t.Fatalf("NextAction(install): %v", err)
	}
	if err := rollout.RecordApplyFailure("executor"); err != nil {
		t.Fatalf("RecordApplyFailure(install): %v", err)
	}
	if _, err := rollout.NextAction(); err != nil {
		t.Fatalf("NextAction(rollback): %v", err)
	}
	if err := rollout.RecordApplyFailure("executor"); !errors.Is(err, update.ErrRollbackFailed) {
		t.Fatalf("RecordApplyFailure(rollback) error = %v, want ErrRollbackFailed", err)
	}
	if rollout.Status() != update.RolloutRollbackFailed || rollout.Nodes()[0].State != update.NodeRollbackFailed {
		t.Fatalf("rollback failure state = %q/%q", rollout.Status(), rollout.Nodes()[0].State)
	}
}

func healthyReceipt(nodeID, version string) update.HealthReceipt {
	return update.HealthReceipt{
		NodeID: nodeID, RunningVersion: version, Started: true, StateReadable: true,
		ProvidersAvailable: true, CoordinatorConnected: true, SessionsAvailable: true, ProbeSucceeded: true,
	}
}
