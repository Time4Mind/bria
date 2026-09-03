package coordinatortransfer_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/computer"
	"bria/internal/coordinator"
	"bria/internal/coordinatortransfer"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/nodelink"
	"bria/internal/settings"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
)

var testNow = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

type memoryStateStore struct {
	mu                sync.Mutex
	candidate         coordinatortransfer.SnapshotReceipt
	active            coordinatortransfer.SnapshotReceipt
	candidateSnapshot coordinatortransfer.Snapshot
	activeSnapshot    coordinatortransfer.Snapshot
	corruptRead       bool
}

func (s *memoryStateStore) Stage(_ context.Context, id string, snapshot coordinatortransfer.Snapshot) (coordinatortransfer.SnapshotReceipt, error) {
	digest, err := snapshot.Digest()
	if err != nil {
		return coordinatortransfer.SnapshotReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.candidate = coordinatortransfer.SnapshotReceipt{TransferID: id, Digest: digest}
	s.candidateSnapshot = snapshot
	return s.candidate, nil
}
func (s *memoryStateStore) Apply(_ context.Context, id, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.candidate.TransferID != id || s.candidate.Digest != digest {
		return errors.New("candidate mismatch")
	}
	s.active = s.candidate
	s.activeSnapshot = s.candidateSnapshot
	return nil
}
func (s *memoryStateStore) Read(context.Context) (coordinatortransfer.Snapshot, coordinatortransfer.SnapshotReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.activeSnapshot
	if s.corruptRead {
		snapshot.Routes = nil
	}
	return snapshot, s.active, nil
}

func TestTargetDoesNotActivateWhenAppliedStateRereadDiffers(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 2}
	old := pairedSource(t, dir, term)
	offer, _ := old.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, ready(term))
	fence, _ := computer.OpenFenceFile(dir + "/fence.json")
	_ = fence.Accept(term)
	state := &memoryStateStore{corruptRead: true}
	target, _ := coordinatortransfer.OpenTarget(dir+"/target.json", "new", "sha256:new", fence, state)
	receipt, _ := target.Stage(context.Background(), offer)
	commit, _ := old.Commit(context.Background(), receipt)
	if _, err := target.Activate(context.Background(), commit); err == nil {
		t.Fatal("target activated after mismatching applied-state reread")
	}
	if target.CanCoordinate() {
		t.Fatal("target became coordinator without verified reread")
	}
}

func validSnapshot() coordinatortransfer.Snapshot {
	created := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	carrier := telegramstate.Carrier{ChatID: 1, MessageID: 10}
	return coordinatortransfer.Snapshot{
		Version:                   coordinatortransfer.SnapshotVersion,
		Catalog:                   computer.CatalogSnapshot{Computers: []computer.Record{{ID: "new", Name: "New", Fingerprint: "sha256:new", Status: computer.StatusOnline, ProtocolVersion: 1}}},
		Routes:                    []coordinatortransfer.Route{{TelegramMessageID: 10, SessionID: "session-1", ComputerID: "new"}},
		Settings:                  settings.Snapshot{Revision: 1, Settings: settings.Default()},
		Sessions:                  []domain.SessionSnapshot{{ID: "session-1", IntentID: "intent-1", ComputerID: "new", Provider: domain.ProviderCodex, Workdir: "/workspace", Status: domain.SessionStarting, CreatedAt: created, StateChangedAt: created}},
		TelegramScope:             coordinatortransfer.TelegramScope{BotID: 100, OwnerUserID: 7, PrivateChatID: 1},
		TelegramUI:                telegramstate.State{Version: telegramstate.FormatVersion, ActiveSession: "session-1", Cards: map[domain.SessionID]telegramstate.Card{"session-1": {SessionID: "session-1", Carrier: carrier, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}}},
		Journals:                  []coordinatortransfer.JournalSession{{SessionID: "session-1", NextSequence: 2}},
		Inputs:                    []messagejournal.Input{{MessageID: "input-1", SessionID: "session-1", Sequence: 1, Phase: messagejournal.InputPending}},
		Outputs:                   []messagejournal.Output{{OperationID: "output-1", SessionID: "session-1", Sequence: 2, Kind: "final", Phase: messagejournal.OutputPending}},
		Checkpoint:                coordinator.StoredCheckpoint{Revision: 1, Checkpoint: coordinator.Checkpoint{Initialized: true, NextUpdateID: 11}},
		CallbackVerificationKeyID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		CallbackRegistry: telegrampipeline.CallbackRegistrySnapshot{Version: telegrampipeline.CallbackRegistrySnapshotVersion, Presentations: map[domain.SessionID]telegrampipeline.CallbackPresentationSnapshot{
			"session-1": {SessionID: "session-1", Carrier: carrier, ExpiresAt: created.Add(time.Hour), Tokens: map[string]bool{"token-a": false}, Claims: map[string]telegrampipeline.CallbackClaimSnapshot{}},
		}},
		CallbackOperations: telegramflow.CallbackStateSnapshot{Version: telegramflow.CallbackStateSnapshotVersion, Operations: map[string]telegramflow.CallbackOperation{}, Statuses: map[string]telegramflow.StatusOperation{}},
	}
}

func ready(term computer.CoordinatorTerm) coordinatortransfer.LiveReceipt {
	digest, _ := validSnapshot().Digest()
	return coordinatortransfer.LiveReceipt{Term: term, Identity: nodelink.ChannelIdentity{LocalComputerID: term.CoordinatorID, PeerComputerID: "new", ExecutorComputerID: "new", PeerCertificateSHA256: "sha256:new", MutuallyAuthenticated: true}, ReadinessID: "ready-1", ProtocolVersion: nodelink.ProtocolVersion, StateDigest: digest, TelegramReady: true, StateStorageReady: true, TelegramScope: coordinatortransfer.TelegramScope{BotID: 100, OwnerUserID: 7, PrivateChatID: 1}, CallbackVerificationKeyID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}
}

func pairedSource(t *testing.T, dir string, term computer.CoordinatorTerm) *coordinatortransfer.Source {
	t.Helper()
	pairings, _ := nodelink.OpenPairingFile(dir + "/pairing.json")
	if pairings.PendingCount() == 0 && !pairings.Authorized("new", "sha256:new") {
		challenge, _ := nodelink.NewPairingChallenge("pair-1", "new", "New", "123456", "sha256:new", testNow.Add(time.Hour))
		_ = pairings.Issue(challenge, testNow)
		_, _ = pairings.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, testNow)
	}
	source, err := coordinatortransfer.OpenSource(dir+"/source.json", term, pairings)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestManualTransferAppliesTypedStateBeforeActivatingExplicitTarget(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 4}
	old := pairedSource(t, dir, term)
	offer, err := old.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, ready(term))
	if err != nil {
		t.Fatal(err)
	}
	fence, _ := computer.OpenFenceFile(dir + "/fence.json")
	_ = fence.Accept(term)
	target, _ := coordinatortransfer.OpenTarget(dir+"/target.json", "new", "sha256:new", fence, &memoryStateStore{})
	receipt, err := target.Stage(context.Background(), offer)
	if err != nil || target.CanCoordinate() {
		t.Fatalf("stage=%#v err=%v active=%v", receipt, err, target.CanCoordinate())
	}
	commit, err := old.Commit(context.Background(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := target.Activate(context.Background(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Finalize(context.Background(), activation); err != nil {
		t.Fatal(err)
	}
	if !target.CanCoordinate() || old.CanCoordinate() {
		t.Fatalf("authority old=%v target=%v", old.CanCoordinate(), target.CanCoordinate())
	}
}

func TestTargetRejectsForgedGenerationAndCrashAmbiguityElectsNobody(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 1}
	old := pairedSource(t, dir, term)
	offer, _ := old.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, ready(term))
	fence, _ := computer.OpenFenceFile(dir + "/fence.json")
	_ = fence.Accept(term)
	target, _ := coordinatortransfer.OpenTarget(dir+"/target.json", "new", "sha256:new", fence, &memoryStateStore{})
	forged := offer
	forged.From.Generation = 9
	forged.To.Generation = 10
	if _, err := target.Stage(context.Background(), forged); err == nil {
		t.Fatal("forged generation jump accepted")
	}
	if _, err := target.Stage(context.Background(), offer); err != nil {
		t.Fatal(err)
	}
	reopenedOld := pairedSource(t, dir, term)
	reopenedTarget, _ := coordinatortransfer.OpenTarget(dir+"/target.json", "new", "sha256:new", fence, &memoryStateStore{})
	if reopenedOld.CanCoordinate() || reopenedTarget.CanCoordinate() {
		t.Fatal("crash ambiguity elected coordinator")
	}
}

func TestPrepareRequiresTelegramAndStorageReadiness(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 1}
	old := pairedSource(t, dir, term)
	receipt := ready(term)
	receipt.TelegramReady = false
	_, err := old.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, receipt)
	if err == nil || !old.CanCoordinate() {
		t.Fatalf("prepare err=%v old=%v", err, old.CanCoordinate())
	}
}

func TestPrepareRequiresSameCallbackVerificationKeyIdentity(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 1}
	old := pairedSource(t, dir, term)
	receipt := ready(term)
	receipt.CallbackVerificationKeyID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, err := old.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, receipt)
	if err == nil || !old.CanCoordinate() {
		t.Fatalf("prepare err=%v old=%v", err, old.CanCoordinate())
	}
}

func TestPrepareRequiresReadinessForExactProtocolAndStateDigest(t *testing.T) {
	for name, mutate := range map[string]func(*coordinatortransfer.LiveReceipt){
		"protocol": func(receipt *coordinatortransfer.LiveReceipt) { receipt.ProtocolVersion++ },
		"digest":   func(receipt *coordinatortransfer.LiveReceipt) { receipt.StateDigest = "different" },
		"tls pin": func(receipt *coordinatortransfer.LiveReceipt) {
			receipt.Identity.PeerCertificateSHA256 = "sha256:other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 1}
			old := pairedSource(t, dir, term)
			receipt := ready(term)
			mutate(&receipt)
			_, err := old.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, receipt)
			if err == nil || !old.CanCoordinate() {
				t.Fatalf("Prepare error=%v old authority=%v", err, old.CanCoordinate())
			}
		})
	}
}

func TestTypedSnapshotRejectsDroppedJournalTombstoneAndMissingCallbackLedger(t *testing.T) {
	dropped := validSnapshot()
	dropped.Inputs = nil
	if err := dropped.Validate(); err == nil {
		t.Fatal("journal gap was accepted")
	}
	missingCallbacks := validSnapshot()
	missingCallbacks.CallbackOperations.Operations = nil
	if err := missingCallbacks.Validate(); err == nil {
		t.Fatal("missing callback operation ledger was accepted")
	}
}

func TestPreparedTransferCanAbortAfterTargetDurablyDropsStage(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 3}
	source := pairedSource(t, dir, term)
	offer, err := source.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, ready(term))
	if err != nil {
		t.Fatal(err)
	}
	fence, _ := computer.OpenFenceFile(dir + "/fence.json")
	_ = fence.Accept(term)
	target, _ := coordinatortransfer.OpenTarget(dir+"/target.json", "new", "sha256:new", fence, &memoryStateStore{})
	if _, err := target.Stage(context.Background(), offer); err != nil {
		t.Fatal(err)
	}
	abortReceipt, err := target.Abort(context.Background(), offer, ready(term).Identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Abort(context.Background(), abortReceipt, fence); err != nil {
		t.Fatal(err)
	}
	reopened := pairedSource(t, dir, term)
	if !reopened.CanCoordinate() || target.CanCoordinate() {
		t.Fatal("aborted handoff did not restore only source authority")
	}
}

func TestExpiredPreCommitQuiesceReclaimsSourceButCommittedAmbiguityDoesNot(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 3}
	pairings, _ := nodelink.OpenPairingFile(dir + "/pairing.json")
	challenge, _ := nodelink.NewPairingChallenge("pair-1", "new", "New", "123456", "sha256:new", testNow.Add(time.Hour))
	_ = pairings.Issue(challenge, testNow)
	_, _ = pairings.Confirm(challenge.ID, challenge.Code, challenge.Fingerprint, testNow)
	now := testNow
	source, err := coordinatortransfer.OpenSourceWithTiming(dir+"/source.json", term, pairings, func() time.Time { return now }, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, ready(term))
	if err != nil {
		t.Fatal(err)
	}
	fence, _ := computer.OpenFenceFile(dir + "/fence.json")
	_ = fence.Accept(term)
	now = now.Add(6 * time.Second)
	if err := source.ReclaimExpired(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	if !source.CanCoordinate() {
		t.Fatal("expired pre-commit quiesce did not restore source")
	}

	second, _ := coordinatortransfer.OpenSourceWithTiming(dir+"/source.json", term, pairings, func() time.Time { return now }, 5*time.Second)
	offer, _ := second.Prepare(context.Background(), coordinatortransfer.Request{ID: "transfer-2", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}, ready(term))
	target, _ := coordinatortransfer.OpenTargetWithClock(dir+"/target.json", "new", "sha256:new", fence, &memoryStateStore{}, func() time.Time { return now })
	receipt, _ := target.Stage(context.Background(), offer)
	if _, err := second.Commit(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	if err := second.ReclaimExpired(context.Background(), fence); err == nil || second.CanCoordinate() {
		t.Fatal("committed ambiguity was silently reclaimed")
	}
}

func TestLostCommitAndActivationResponsesAreExactlyReplayableAfterRestart(t *testing.T) {
	dir := t.TempDir()
	term := computer.CoordinatorTerm{CoordinatorID: "old", Generation: 7}
	source := pairedSource(t, dir, term)
	request := coordinatortransfer.Request{ID: "transfer-1", TargetID: "new", TargetFingerprint: "sha256:new", Snapshot: validSnapshot()}
	offer, _ := source.Prepare(context.Background(), request, ready(term))
	replayedOffer, err := source.Prepare(context.Background(), request, ready(term))
	if err != nil || replayedOffer.StateDigest != offer.StateDigest {
		t.Fatalf("prepare retry=%#v err=%v", replayedOffer, err)
	}
	fence, _ := computer.OpenFenceFile(dir + "/fence.json")
	_ = fence.Accept(term)
	state := &memoryStateStore{}
	target, _ := coordinatortransfer.OpenTarget(dir+"/target.json", "new", "sha256:new", fence, state)
	receipt, _ := target.Stage(context.Background(), offer)
	if replayed, err := target.Stage(context.Background(), offer); err != nil || replayed != receipt {
		t.Fatalf("stage retry=%#v err=%v", replayed, err)
	}
	wantCommit, err := source.Commit(context.Background(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSource := pairedSource(t, dir, term)
	gotCommit, err := reopenedSource.Commit(context.Background(), receipt)
	if err != nil || !sameCommit(wantCommit, gotCommit) {
		t.Fatalf("commit retry=%#v err=%v", gotCommit, err)
	}
	wantActivation, err := target.Activate(context.Background(), gotCommit)
	if err != nil {
		t.Fatal(err)
	}
	reopenedTarget, _ := coordinatortransfer.OpenTarget(dir+"/target.json", "new", "sha256:new", fence, state)
	gotActivation, err := reopenedTarget.Activate(context.Background(), gotCommit)
	if err != nil || gotActivation != wantActivation {
		t.Fatalf("activation retry=%#v err=%v", gotActivation, err)
	}
	if err := reopenedSource.Finalize(context.Background(), gotActivation); err != nil {
		t.Fatal(err)
	}
	if err := reopenedSource.Finalize(context.Background(), gotActivation); err != nil {
		t.Fatalf("finalize retry=%v", err)
	}
	conflict := receipt
	conflict.StateDigest = "different"
	if _, err := reopenedSource.Commit(context.Background(), conflict); err == nil {
		t.Fatal("conflicting commit retry accepted")
	}
}

func sameCommit(left, right coordinatortransfer.Commit) bool {
	return left.Offer.ID == right.Offer.ID && left.Offer.StateDigest == right.Offer.StateDigest && left.Offer.To == right.Offer.To
}
