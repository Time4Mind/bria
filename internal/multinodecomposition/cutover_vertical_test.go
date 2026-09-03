package multinodecomposition_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/computer"
	"bria/internal/coordinator"
	"bria/internal/coordinatorbundle"
	"bria/internal/coordinatorstate"
	"bria/internal/coordinatortransfer"
	"bria/internal/domain"
	"bria/internal/multinodecomposition"
	"bria/internal/nodelink"
	"bria/internal/settings"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
)

func runManualCutoverPhysicallyReopensExactStateAndFence(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 3}
	snapshot := cutoverSnapshot()
	source, pairings := cutoverSource(t, dir, term)
	fence, _ := computer.OpenFenceFile(filepath.Join(dir, "fence.json"))
	if err := fence.Accept(term); err != nil {
		t.Fatal(err)
	}
	state, err := coordinatorstate.Open(filepath.Join(dir, "coordinator-state"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := coordinatortransfer.OpenTarget(filepath.Join(dir, "target.json"), "new", "sha256:new", fence, state)
	if err != nil {
		t.Fatal(err)
	}
	request := coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: snapshot}
	activation, err := multinodecomposition.ManualCutover(context.Background(), source, target, request, cutoverReadiness(term, snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if activation.Term != (computer.CoordinatorTerm{CoordinatorID: "new", Generation: 4}) {
		t.Fatalf("activation=%#v", activation)
	}

	reopenedFence, err := computer.OpenFenceFile(filepath.Join(dir, "fence.json"))
	if err != nil {
		t.Fatal(err)
	}
	reopenedState, err := coordinatorstate.Open(filepath.Join(dir, "coordinator-state"))
	if err != nil {
		t.Fatal(err)
	}
	reopenedSource, err := coordinatortransfer.OpenSource(filepath.Join(dir, "source.json"), term, pairings)
	if err != nil {
		t.Fatal(err)
	}
	reopenedTarget, err := coordinatortransfer.OpenTarget(filepath.Join(dir, "target.json"), "new", "sha256:new", reopenedFence, reopenedState)
	if err != nil {
		t.Fatal(err)
	}
	got, receipt, err := reopenedState.Read(context.Background())
	gotDigest, digestErr := got.Digest()
	wantDigest, _ := snapshot.Digest()
	if err != nil || digestErr != nil || receipt.TransferID != request.ID || receipt.Digest != wantDigest || gotDigest != wantDigest {
		t.Fatalf("physical state receipt=%#v digest=%q err=%v digestErr=%v", receipt, gotDigest, err, digestErr)
	}
	if reopenedSource.CanCoordinate() || !reopenedTarget.CanCoordinate() {
		t.Fatalf("physical authority old=%v new=%v", reopenedSource.CanCoordinate(), reopenedTarget.CanCoordinate())
	}
	if err := reopenedFence.Validate(activation.Term); err != nil {
		t.Fatalf("new fence rejected: %v", err)
	}
	if err := reopenedFence.Validate(term); !errors.Is(err, computer.ErrStaleGeneration) {
		t.Fatalf("old fence error=%v", err)
	}
}

func TestCommittedCutoverCrashReopensWithNoElectionAndResumesExactCommit(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 8}
	snapshot := cutoverSnapshot()
	source, pairings := cutoverSource(t, dir, term)
	fence, _ := computer.OpenFenceFile(filepath.Join(dir, "fence.json"))
	_ = fence.Accept(term)
	state, _ := coordinatorstate.Open(filepath.Join(dir, "coordinator-state"))
	target, _ := coordinatortransfer.OpenTarget(filepath.Join(dir, "target.json"), "new", "sha256:new", fence, state)
	request := coordinatortransfer.Request{ID: "transfer-crash", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: snapshot}
	offer, err := source.Prepare(context.Background(), request, cutoverReadiness(term, snapshot))
	if err != nil {
		t.Fatal(err)
	}
	stage, err := target.Stage(context.Background(), offer)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := source.Commit(context.Background(), stage)
	if err != nil {
		t.Fatal(err)
	}

	reopenedFence, _ := computer.OpenFenceFile(filepath.Join(dir, "fence.json"))
	reopenedState, _ := coordinatorstate.Open(filepath.Join(dir, "coordinator-state"))
	reopenedSource, _ := coordinatortransfer.OpenSource(filepath.Join(dir, "source.json"), term, pairings)
	reopenedTarget, _ := coordinatortransfer.OpenTarget(filepath.Join(dir, "target.json"), "new", "sha256:new", reopenedFence, reopenedState)
	if reopenedSource.CanCoordinate() || reopenedTarget.CanCoordinate() {
		t.Fatal("commit/activation crash elected a coordinator")
	}
	activation, err := reopenedTarget.Activate(context.Background(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopenedSource.Finalize(context.Background(), activation); err != nil {
		t.Fatal(err)
	}
	physicalFence, _ := computer.OpenFenceFile(filepath.Join(dir, "fence.json"))
	physicalState, _ := coordinatorstate.Open(filepath.Join(dir, "coordinator-state"))
	physicalTarget, _ := coordinatortransfer.OpenTarget(filepath.Join(dir, "target.json"), "new", "sha256:new", physicalFence, physicalState)
	if !physicalTarget.CanCoordinate() || physicalFence.Validate(activation.Term) != nil {
		t.Fatal("explicit crash resume did not activate the exact committed target")
	}
}

func cutoverSource(t *testing.T, dir string, term computer.CoordinatorTerm) (*coordinatortransfer.Source, *nodelink.PairingFile) {
	t.Helper()
	pairings, err := nodelink.OpenPairingFile(filepath.Join(dir, "pairings.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	challenge, err := nodelink.NewPairingChallenge("pair-1", "new", "New", "123456", "sha256:new", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := pairings.Issue(challenge, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pairings.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, now); err != nil {
		t.Fatal(err)
	}
	source, err := coordinatortransfer.OpenSource(filepath.Join(dir, "source.json"), term, pairings)
	if err != nil {
		t.Fatal(err)
	}
	return source, pairings
}

func cutoverReadiness(term computer.CoordinatorTerm, snapshot coordinatorbundle.Bundle) coordinatortransfer.LiveReceipt {
	digest, _ := snapshot.Digest()
	return coordinatortransfer.LiveReceipt{
		Term: term, Identity: nodelink.ChannelIdentity{LocalComputerID: "old", PeerComputerID: "new", ExecutorComputerID: "new", PeerCertificateSHA256: "sha256:new", MutuallyAuthenticated: true},
		ReadinessID: "ready-1", ProtocolVersion: nodelink.ProtocolVersion, StateDigest: digest,
		TelegramReady: true, StateStorageReady: true, TelegramScope: snapshot.TelegramScope,
		CallbackVerificationKeyID: snapshot.CallbackVerificationKeyID,
	}
}

func cutoverSnapshot() coordinatorbundle.Bundle {
	return coordinatorbundle.Bundle{
		Version: coordinatorbundle.Version,
		Catalog: computer.CatalogSnapshot{Computers: []computer.Record{
			{ID: "old", Name: "Old", Fingerprint: "sha256:old", Status: computer.StatusOnline, ProtocolVersion: nodelink.ProtocolVersion},
			{ID: "new", Name: "New", Fingerprint: "sha256:new", Status: computer.StatusOnline, ProtocolVersion: nodelink.ProtocolVersion},
		}},
		Settings:      settings.Snapshot{Revision: 1, Settings: settings.Default()},
		TelegramScope: coordinatorbundle.TelegramScope{BotID: 100, OwnerUserID: 7, PrivateChatID: 1},
		TelegramUI:    telegramstate.New(),
		Checkpoint: coordinator.StoredCheckpoint{Revision: 1, Checkpoint: coordinator.Checkpoint{
			Initialized: true, NextUpdateID: 11,
		}},
		CallbackVerificationKeyID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		CallbackRegistry: telegrampipeline.CallbackRegistrySnapshot{
			Version: telegrampipeline.CallbackRegistrySnapshotVersion, Presentations: map[domain.SessionID]telegrampipeline.CallbackPresentationSnapshot{},
		},
		CallbackOperations: telegramflow.CallbackStateSnapshot{
			Version: telegramflow.CallbackStateSnapshotVersion, Operations: map[string]telegramflow.CallbackOperation{}, Statuses: map[string]telegramflow.StatusOperation{},
		},
	}
}
