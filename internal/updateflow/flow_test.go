package updateflow_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"bria/internal/update"
	"bria/internal/updateflow"
)

func TestStartVerifiesReleaseThenUpdatesExecutorsSequentiallyAndCoordinatorLast(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, publicKey, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}:  []byte("linux-release"),
		{os: "darwin", arch: "arm64"}: []byte("darwin-release"),
	})
	store := newMemoryStore()
	service := updateflow.Service{
		Source:      &fakeSource{signed: signed, payloads: payloads, events: events},
		Stager:      &fakeStager{events: events},
		Installer:   &fakeInstaller{events: events},
		Postflight:  &fakePostflight{events: events},
		Store:       store,
		TrustedKeys: update.TrustedKeys{"owner": publicKey},
	}
	request := updateflow.Request{FlowID: "release-2", Targets: []updateflow.Target{
		{Node: onlineNode("coordinator", update.RoleCoordinator, "1.0.0"), Platform: "darwin", Arch: "arm64", PriorState: "coordinator-state-v1"},
		{Node: onlineNode("executor", update.RoleExecutor, "1.1.0"), Platform: "linux", Arch: "amd64", PriorState: "executor-state-v1"},
	}}

	state, err := service.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state.Phase != updateflow.PhaseCompleted || state.Rollout.Status() != update.RolloutCompleted {
		t.Fatalf("completed state = phase %q rollout %q", state.Phase, state.Rollout.Status())
	}
	wantEvents := []string{
		"manifest",
		"artifact:linux/amd64", "stage:executor:linux/amd64", "install:executor:1.1.0->2.0.0", "probe:executor:2.0.0",
		"artifact:darwin/arm64", "stage:coordinator:darwin/arm64", "install:coordinator:1.0.0->2.0.0", "probe:coordinator:2.0.0",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("events = %#v, want %#v", got, wantEvents)
	}
	persisted, found, err := store.Load(context.Background(), request.FlowID)
	if err != nil || !found {
		t.Fatalf("Load = found %v, err %v", found, err)
	}
	if persisted.Phase != updateflow.PhaseCompleted || persisted.Rollout.Order()[1] != "coordinator" {
		t.Fatalf("persisted result = %#v", persisted)
	}
}

func TestOfflineNodeStopsDurablyAndOnlyExplicitOnlineResumeContinues(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, publicKey, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}: []byte("linux-release"),
	})
	store := newMemoryStore()
	service := updateflow.Service{
		Source: &fakeSource{signed: signed, payloads: payloads, events: events},
		Stager: &fakeStager{events: events}, Installer: &fakeInstaller{events: events},
		Postflight: &fakePostflight{events: events}, Store: store,
		TrustedKeys: update.TrustedKeys{"owner": publicKey},
	}
	request := updateflow.Request{FlowID: "offline-flow", Targets: []updateflow.Target{
		{Node: update.Node{ID: "executor", Role: update.RoleExecutor, CurrentVersion: "1.0.0", Availability: update.AvailabilityOffline}, Platform: "linux", Arch: "amd64", PriorState: "executor-state-v1"},
		{Node: onlineNode("coordinator", update.RoleCoordinator, "1.0.0"), Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1"},
	}}

	stopped, err := service.Start(context.Background(), request)
	if !errors.Is(err, update.ErrNodeUnavailable) {
		t.Fatalf("Start error = %v, want ErrNodeUnavailable", err)
	}
	if stopped.Phase != updateflow.PhaseStopped || stopped.Rollout.Status() != update.RolloutStopped {
		t.Fatalf("stopped state = phase %q rollout %q", stopped.Phase, stopped.Rollout.Status())
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, []string{"manifest"}) {
		t.Fatalf("events before explicit resume = %#v", got)
	}

	completed, err := service.Resume(context.Background(), request.FlowID, update.AvailabilityOnline)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if completed.Phase != updateflow.PhaseCompleted {
		t.Fatalf("resumed phase = %q, want completed", completed.Phase)
	}
	if got := events.snapshot(); len(got) != 9 || got[1] != "artifact:linux/amd64" || got[6] != "stage:coordinator:linux/amd64" {
		t.Fatalf("events after explicit resume = %#v", got)
	}
}

func TestUnhealthyTargetRollsBackAndVerifiesPriorVersionAndStateBeforeStopping(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, publicKey, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}: []byte("linux-release"),
	})
	postflight := &fakePostflight{events: events}
	postflight.probe = func(nodeID, version string) (updateflow.PostflightReceipt, error) {
		if version == "2.0.0" {
			return updateflow.PostflightReceipt{Health: healthyReceipt(nodeID, "2.0.1"), StateFingerprint: "new-state"}, nil
		}
		return updateflow.PostflightReceipt{Health: healthyReceipt(nodeID, "1.1.0"), StateFingerprint: "executor-state-v1"}, nil
	}
	store := newMemoryStore()
	service := updateflow.Service{
		Source: &fakeSource{signed: signed, payloads: payloads, events: events},
		Stager: &fakeStager{events: events}, Installer: &fakeInstaller{events: events},
		Postflight: postflight, Store: store, TrustedKeys: update.TrustedKeys{"owner": publicKey},
	}
	request := updateflow.Request{FlowID: "rollback-flow", Targets: []updateflow.Target{
		{Node: onlineNode("coordinator", update.RoleCoordinator, "1.0.0"), Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1"},
		{Node: onlineNode("executor", update.RoleExecutor, "1.1.0"), Platform: "linux", Arch: "amd64", PriorState: "executor-state-v1"},
	}}

	state, err := service.Start(context.Background(), request)
	if !errors.Is(err, updateflow.ErrUpdateRolledBack) {
		t.Fatalf("Start error = %v, want ErrUpdateRolledBack", err)
	}
	if state.Phase != updateflow.PhaseStopped || state.Rollout.Status() != update.RolloutStopped {
		t.Fatalf("rollback state = phase %q rollout %q", state.Phase, state.Rollout.Status())
	}
	nodes := state.Rollout.Nodes()
	if nodes[0].ID != "executor" || nodes[0].State != update.NodeRolledBack || nodes[0].CurrentVersion != "1.1.0" || nodes[1].State != update.NodePending {
		t.Fatalf("nodes after rollback = %#v", nodes)
	}
	wantEvents := []string{
		"manifest", "artifact:linux/amd64", "stage:executor:linux/amd64",
		"install:executor:1.1.0->2.0.0", "probe:executor:2.0.0",
		"rollback:executor:2.0.0->1.1.0", "probe:executor:1.1.0",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("rollback events = %#v, want %#v", got, wantEvents)
	}
}

func TestAdvancePersistsCompletedExecutorAndReopenContinuesWithCoordinator(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, publicKey, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}: []byte("linux-release"),
	})
	directory := filepath.Join(t.TempDir(), "flows")
	store, err := updateflow.OpenFileStore(directory)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	newService := func(store updateflow.Store) updateflow.Service {
		return updateflow.Service{
			Source: &fakeSource{signed: signed, payloads: payloads, events: events},
			Stager: &fakeStager{events: events}, Installer: &fakeInstaller{events: events},
			Postflight: &fakePostflight{events: events}, Store: store,
			TrustedKeys: update.TrustedKeys{"owner": publicKey},
		}
	}
	request := updateflow.Request{FlowID: "reopen-flow", Targets: []updateflow.Target{
		{Node: onlineNode("coordinator", update.RoleCoordinator, "1.0.0"), Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1"},
		{Node: onlineNode("executor", update.RoleExecutor, "1.0.0"), Platform: "linux", Arch: "amd64", PriorState: "executor-state-v1"},
	}}
	service := newService(store)
	if _, err := service.Prepare(context.Background(), request); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	afterExecutor, err := service.Advance(context.Background(), request.FlowID)
	if err != nil {
		t.Fatalf("Advance executor: %v", err)
	}
	if afterExecutor.Rollout.CurrentIndex != 1 || afterExecutor.Rollout.Nodes()[0].State != update.NodeUpdated || afterExecutor.Phase != updateflow.PhasePrepared {
		t.Fatalf("after executor = %#v", afterExecutor)
	}

	reopened, err := updateflow.OpenFileStore(directory)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	completed, err := newService(reopened).Run(context.Background(), request.FlowID)
	if err != nil {
		t.Fatalf("Run after reopen: %v", err)
	}
	if completed.Phase != updateflow.PhaseCompleted || completed.Rollout.CurrentIndex != 2 {
		t.Fatalf("completed after reopen = %#v", completed)
	}
	wantEvents := []string{
		"manifest", "artifact:linux/amd64", "stage:executor:linux/amd64", "install:executor:1.0.0->2.0.0", "probe:executor:2.0.0",
		"artifact:linux/amd64", "stage:coordinator:linux/amd64", "install:coordinator:1.0.0->2.0.0", "probe:coordinator:2.0.0",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("reopen events = %#v, want %#v", got, wantEvents)
	}
}

func TestIncompleteStageNeverInvokesInstallerOrRollback(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, publicKey, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}: []byte("linux-release"),
	})
	service := updateflow.Service{
		Source: &fakeSource{signed: signed, payloads: payloads, events: events},
		Stager: partialStager{events: events}, Installer: &fakeInstaller{events: events},
		Postflight: &fakePostflight{events: events}, Store: newMemoryStore(),
		TrustedKeys: update.TrustedKeys{"owner": publicKey},
	}
	request := updateflow.Request{FlowID: "partial-stage", Targets: []updateflow.Target{{
		Node:     onlineNode("coordinator", update.RoleCoordinator, "1.0.0"),
		Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1",
	}}}

	state, err := service.Start(context.Background(), request)
	if !errors.Is(err, updateflow.ErrStageUnknown) {
		t.Fatalf("Start error = %v, want ErrStageUnknown", err)
	}
	if state.Phase != updateflow.PhasePrepared || state.Rollout.Nodes()[0].State != update.NodePending {
		t.Fatalf("state after incomplete staging = %#v", state)
	}
	if got, want := events.snapshot(), []string{"manifest", "artifact:linux/amd64", "stage-partial:coordinator"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestUntrustedManifestStopsBeforeArtifactFetchOrAnyPlatformOperation(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, _, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}: []byte("linux-release"),
	})
	otherPublic, _, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{9}, 64)))
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	store := newMemoryStore()
	service := updateflow.Service{
		Source: &fakeSource{signed: signed, payloads: payloads, events: events},
		Stager: &fakeStager{events: events}, Installer: &fakeInstaller{events: events},
		Postflight: &fakePostflight{events: events}, Store: store,
		TrustedKeys: update.TrustedKeys{"owner": otherPublic},
	}
	request := updateflow.Request{FlowID: "untrusted-release", Targets: []updateflow.Target{{
		Node:     onlineNode("coordinator", update.RoleCoordinator, "1.0.0"),
		Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1",
	}}}

	if _, err := service.Start(context.Background(), request); !errors.Is(err, update.ErrInvalidSignature) {
		t.Fatalf("Start error = %v, want ErrInvalidSignature", err)
	}
	if got, want := events.snapshot(), []string{"manifest"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if _, found, err := store.Load(context.Background(), request.FlowID); err != nil || found {
		t.Fatalf("untrusted flow persisted = found %v, err %v", found, err)
	}
}

func TestStageSaveFailureRemainsUnknownAcrossReopenUntilExplicitRetry(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, publicKey, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}: []byte("linux-release"),
	})
	baseStore := newMemoryStore()
	store := &failStageReceiptStore{inner: baseStore}
	stager := &capturingStager{events: events}
	service := updateflow.Service{
		Source: &fakeSource{signed: signed, payloads: payloads, events: events},
		Stager: stager, Installer: &fakeInstaller{events: events},
		Postflight: &fakePostflight{events: events}, Store: store,
		TrustedKeys: update.TrustedKeys{"owner": publicKey},
	}
	request := updateflow.Request{FlowID: "stage-save-failure", Targets: []updateflow.Target{{
		Node:     onlineNode("coordinator", update.RoleCoordinator, "1.0.0"),
		Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1",
	}}}

	if _, err := service.Start(context.Background(), request); !errors.Is(err, errInjectedStageSave) {
		t.Fatalf("Start error = %v, want injected stage save failure", err)
	}
	if len(stager.operationIDs) != 1 || stager.operationIDs[0] == "" {
		t.Fatalf("first stage operation ids = %#v", stager.operationIDs)
	}
	if _, err := service.Run(context.Background(), request.FlowID); !errors.Is(err, updateflow.ErrStageUnknown) {
		t.Fatalf("Run after reopen error = %v, want ErrStageUnknown", err)
	}
	if len(stager.operationIDs) != 1 {
		t.Fatalf("automatic reopen repeated stage: ids %#v", stager.operationIDs)
	}

	if _, err := service.RetryStage(context.Background(), request.FlowID); err != nil {
		t.Fatalf("RetryStage: %v", err)
	}
	if len(stager.operationIDs) != 2 || stager.operationIDs[1] != stager.operationIDs[0] {
		t.Fatalf("explicit retry operation ids = %#v, want same stable id", stager.operationIDs)
	}
	completed, err := service.Run(context.Background(), request.FlowID)
	if err != nil {
		t.Fatalf("Run after explicit stage retry: %v", err)
	}
	if completed.Phase != updateflow.PhaseCompleted {
		t.Fatalf("phase after explicit retry = %q", completed.Phase)
	}
}

func TestInstallCannotCompleteWhenPriorStateFingerprintWasNotPreserved(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, publicKey, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}: []byte("linux-release"),
	})
	installer := &fakeInstaller{events: events}
	installer.install = func(request updateflow.InstallRequest) (updateflow.InstallReceipt, error) {
		return updateflow.InstallReceipt{
			OperationID: request.OperationID, NodeID: request.NodeID,
			RunningVersion: request.TargetVersion, State: "lost-state", Applied: true,
		}, nil
	}
	postflight := &fakePostflight{events: events}
	postflight.probe = func(nodeID, version string) (updateflow.PostflightReceipt, error) {
		if version == "2.0.0" {
			return updateflow.PostflightReceipt{Health: healthyReceipt(nodeID, version), StateFingerprint: "lost-state"}, nil
		}
		return updateflow.PostflightReceipt{Health: healthyReceipt(nodeID, "1.0.0"), StateFingerprint: "coordinator-state-v1"}, nil
	}
	service := updateflow.Service{
		Source: &fakeSource{signed: signed, payloads: payloads, events: events},
		Stager: &fakeStager{events: events}, Installer: installer, Postflight: postflight,
		Store: newMemoryStore(), TrustedKeys: update.TrustedKeys{"owner": publicKey},
	}
	request := updateflow.Request{FlowID: "state-loss", Targets: []updateflow.Target{{
		Node:     onlineNode("coordinator", update.RoleCoordinator, "1.0.0"),
		Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1",
	}}}

	state, err := service.Start(context.Background(), request)
	if !errors.Is(err, updateflow.ErrUpdateRolledBack) {
		t.Fatalf("Start error = %v, want ErrUpdateRolledBack", err)
	}
	if state.Rollout.Nodes()[0].State != update.NodeRolledBack {
		t.Fatalf("state-losing install result = %#v", state.Rollout.Nodes())
	}
}

func TestAmbiguousRollbackIsNotAutomaticallyRepeatedAfterReopen(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	signed, publicKey, payloads := signedRelease(t, "2.0.0", map[platform][]byte{
		{os: "linux", arch: "amd64"}: []byte("linux-release"),
	})
	installer := &fakeInstaller{events: events}
	var rollbackOperations []string
	installer.rollback = func(request updateflow.RollbackRequest) (updateflow.InstallReceipt, error) {
		rollbackOperations = append(rollbackOperations, request.OperationID)
		if len(rollbackOperations) == 1 {
			return updateflow.InstallReceipt{}, errors.New("connection lost after rollback")
		}
		return updateflow.InstallReceipt{
			OperationID: request.OperationID, NodeID: request.NodeID,
			RunningVersion: request.TargetVersion, State: request.TargetState, Applied: true,
		}, nil
	}
	postflight := &fakePostflight{events: events}
	postflight.probe = func(nodeID, version string) (updateflow.PostflightReceipt, error) {
		if version == "2.0.0" {
			return updateflow.PostflightReceipt{Health: healthyReceipt(nodeID, "broken"), StateFingerprint: "coordinator-state-v1"}, nil
		}
		return updateflow.PostflightReceipt{Health: healthyReceipt(nodeID, "1.0.0"), StateFingerprint: "coordinator-state-v1"}, nil
	}
	service := updateflow.Service{
		Source: &fakeSource{signed: signed, payloads: payloads, events: events},
		Stager: &fakeStager{events: events}, Installer: installer, Postflight: postflight,
		Store: newMemoryStore(), TrustedKeys: update.TrustedKeys{"owner": publicKey},
	}
	request := updateflow.Request{FlowID: "rollback-unknown", Targets: []updateflow.Target{{
		Node:     onlineNode("coordinator", update.RoleCoordinator, "1.0.0"),
		Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1",
	}}}

	state, err := service.Start(context.Background(), request)
	if !errors.Is(err, updateflow.ErrRollbackUnknown) {
		t.Fatalf("Start error = %v, want ErrRollbackUnknown", err)
	}
	if state.Phase != updateflow.PhaseRollingBack || state.Rollout.Nodes()[0].State != update.NodeRollingBack {
		t.Fatalf("ambiguous rollback state = %#v", state)
	}
	if _, err := service.Run(context.Background(), request.FlowID); !errors.Is(err, updateflow.ErrRollbackUnknown) {
		t.Fatalf("Run after reopen error = %v, want ErrRollbackUnknown", err)
	}
	if len(rollbackOperations) != 1 {
		t.Fatalf("automatic rollback repeat operations = %#v", rollbackOperations)
	}
	if _, err := service.RetryRollback(context.Background(), request.FlowID); err != nil {
		t.Fatalf("RetryRollback: %v", err)
	}
	if len(rollbackOperations) != 2 || rollbackOperations[0] != rollbackOperations[1] {
		t.Fatalf("explicit rollback operations = %#v, want same stable id", rollbackOperations)
	}
	state, err = service.Run(context.Background(), request.FlowID)
	if !errors.Is(err, updateflow.ErrUpdateRolledBack) || state.Phase != updateflow.PhaseStopped {
		t.Fatalf("verified rollback result = phase %q err %v", state.Phase, err)
	}
}

type platform struct {
	os   string
	arch string
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type seekReadCloser struct{ *bytes.Reader }

func (seekReadCloser) Close() error { return nil }

type fakeSource struct {
	signed   []byte
	payloads map[platform][]byte
	events   *eventLog
}

func (s *fakeSource) SignedManifest(context.Context) ([]byte, error) {
	s.events.add("manifest")
	return append([]byte(nil), s.signed...), nil
}

func (s *fakeSource) Artifact(_ context.Context, artifact update.Artifact) (updateflow.ArtifactPayload, error) {
	s.events.add("artifact:" + artifact.Platform + "/" + artifact.Arch)
	payload, found := s.payloads[platform{os: artifact.Platform, arch: artifact.Arch}]
	if !found {
		return nil, errors.New("missing test payload")
	}
	return seekReadCloser{bytes.NewReader(payload)}, nil
}

type fakeStager struct{ events *eventLog }

func (s *fakeStager) Stage(_ context.Context, request updateflow.StageRequest) (updateflow.StageReceipt, error) {
	if _, err := io.Copy(io.Discard, request.Content); err != nil {
		return updateflow.StageReceipt{}, err
	}
	s.events.add("stage:" + request.NodeID + ":" + request.Artifact.Platform + "/" + request.Artifact.Arch)
	return updateflow.StageReceipt{
		OperationID: request.OperationID, NodeID: request.NodeID, Version: request.Version, Artifact: request.Artifact,
		Reference: "stage-" + request.NodeID,
	}, nil
}

type partialStager struct{ events *eventLog }

func (s partialStager) Stage(_ context.Context, request updateflow.StageRequest) (updateflow.StageReceipt, error) {
	buffer := make([]byte, request.Artifact.Size)
	if _, err := io.ReadFull(request.Content, buffer); err != nil {
		return updateflow.StageReceipt{}, err
	}
	s.events.add("stage-partial:" + request.NodeID)
	return updateflow.StageReceipt{
		OperationID: request.OperationID, NodeID: request.NodeID, Version: request.Version, Artifact: request.Artifact, Reference: "partial-stage",
	}, nil
}

type capturingStager struct {
	events       *eventLog
	operationIDs []string
}

func (s *capturingStager) Stage(_ context.Context, request updateflow.StageRequest) (updateflow.StageReceipt, error) {
	if _, err := io.Copy(io.Discard, request.Content); err != nil {
		return updateflow.StageReceipt{}, err
	}
	s.operationIDs = append(s.operationIDs, request.OperationID)
	s.events.add("stage:" + request.NodeID)
	return updateflow.StageReceipt{
		OperationID: request.OperationID, NodeID: request.NodeID, Version: request.Version,
		Artifact: request.Artifact, Reference: "stage-" + request.NodeID,
	}, nil
}

type fakeInstaller struct {
	events   *eventLog
	install  func(updateflow.InstallRequest) (updateflow.InstallReceipt, error)
	rollback func(updateflow.RollbackRequest) (updateflow.InstallReceipt, error)
}

func (i *fakeInstaller) Install(_ context.Context, request updateflow.InstallRequest) (updateflow.InstallReceipt, error) {
	i.events.add("install:" + request.NodeID + ":" + request.FromVersion + "->" + request.TargetVersion)
	if i.install != nil {
		return i.install(request)
	}
	return updateflow.InstallReceipt{
		OperationID: request.OperationID, NodeID: request.NodeID,
		RunningVersion: request.TargetVersion, State: request.PriorState, Applied: true,
	}, nil
}

func (i *fakeInstaller) Rollback(_ context.Context, request updateflow.RollbackRequest) (updateflow.InstallReceipt, error) {
	i.events.add("rollback:" + request.NodeID + ":" + request.FromVersion + "->" + request.TargetVersion)
	if i.rollback != nil {
		return i.rollback(request)
	}
	return updateflow.InstallReceipt{
		OperationID: request.OperationID, NodeID: request.NodeID,
		RunningVersion: request.TargetVersion, State: request.TargetState, Applied: true,
	}, nil
}

type fakePostflight struct {
	events *eventLog
	probe  func(string, string) (updateflow.PostflightReceipt, error)
}

func (p *fakePostflight) Probe(_ context.Context, nodeID, version string) (updateflow.PostflightReceipt, error) {
	p.events.add("probe:" + nodeID + ":" + version)
	if p.probe != nil {
		return p.probe(nodeID, version)
	}
	return updateflow.PostflightReceipt{Health: healthyReceipt(nodeID, version), StateFingerprint: nodeID + "-state-v1"}, nil
}

type memoryStore struct {
	mu     sync.Mutex
	states map[string]updateflow.State
}

var errInjectedStageSave = errors.New("injected stage receipt save failure")

type failStageReceiptStore struct {
	inner  *memoryStore
	failed bool
}

func (s *failStageReceiptStore) Load(ctx context.Context, id string) (updateflow.State, bool, error) {
	return s.inner.Load(ctx, id)
}

func (s *failStageReceiptStore) Save(ctx context.Context, state updateflow.State) error {
	confirmedOperation := false
	for _, operation := range state.StageOperations {
		confirmedOperation = confirmedOperation || operation.Status == updateflow.StageConfirmed
	}
	if !s.failed && confirmedOperation {
		s.failed = true
		return errInjectedStageSave
	}
	return s.inner.Save(ctx, state)
}

func newMemoryStore() *memoryStore { return &memoryStore{states: make(map[string]updateflow.State)} }

func (s *memoryStore) Load(_ context.Context, id string) (updateflow.State, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, found := s.states[id]
	return cloneState(state), found, nil
}

func (s *memoryStore) Save(_ context.Context, state updateflow.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.states[state.FlowID]
	if found {
		if current.Revision == state.Revision && reflect.DeepEqual(current, state) {
			return nil
		}
		if current.Revision+1 != state.Revision {
			return updateflow.ErrRevisionConflict
		}
	} else if state.Revision != 1 {
		return updateflow.ErrRevisionConflict
	}
	s.states[state.FlowID] = cloneState(state)
	return nil
}

func cloneState(state updateflow.State) updateflow.State {
	encoded, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	var cloned updateflow.State
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		panic(err)
	}
	return cloned
}

func signedRelease(t *testing.T, version string, payloads map[platform][]byte) ([]byte, ed25519.PublicKey, map[platform][]byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{7}, 64)))
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sources := make([]update.ArtifactSource, 0, len(payloads))
	for target, payload := range payloads {
		sources = append(sources, update.ArtifactSource{
			Name: "bria-" + target.os + "-" + target.arch, Platform: target.os, Arch: target.arch,
			Content: bytes.NewReader(payload),
		})
	}
	manifest, err := update.BuildManifest(version, sources)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	signed, err := update.SignManifest(manifest, "owner", privateKey)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	return signed, publicKey, payloads
}

func onlineNode(id string, role update.Role, version string) update.Node {
	return update.Node{ID: id, Role: role, CurrentVersion: version, Availability: update.AvailabilityOnline}
}

func healthyReceipt(nodeID, version string) update.HealthReceipt {
	return update.HealthReceipt{
		NodeID: nodeID, RunningVersion: version, Started: true, StateReadable: true,
		ProvidersAvailable: true, CoordinatorConnected: true, SessionsAvailable: true, ProbeSucceeded: true,
	}
}
