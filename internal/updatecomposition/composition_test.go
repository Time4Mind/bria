package updatecomposition_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"bria/internal/update"
	"bria/internal/updatecomposition"
	"bria/internal/updateflow"
	"bria/internal/updateinstall"
)

func TestInstalledStateFingerprintsUsesInstallerStateContract(t *testing.T) {
	t.Parallel()
	producer := updatecomposition.InstalledStateFingerprints{Reader: fixedInstalledState{state: updateinstall.InstalledState{
		Version: "1.0.0", StateFingerprint: "state-v1",
	}}}
	got, err := producer.StateFingerprint(context.Background(), "node")
	if err != nil {
		t.Fatalf("StateFingerprint: %v", err)
	}
	if got != "state-v1" {
		t.Fatalf("state fingerprint = %q, want state-v1", got)
	}
}

func TestCompositionManualTriggerCapturesFreshFingerprintsAndPreservesExecutorFirstOrder(t *testing.T) {
	t.Parallel()
	service := &recordingFlowService{}
	composition, err := updatecomposition.Open(updatecomposition.Config{
		Service: service,
		Targets: updatecomposition.StaticTargetSource{Values: []updatecomposition.Target{
			{Node: update.Node{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline}, Platform: "linux", Arch: "amd64"},
			{Node: update.Node{ID: "executor", Role: update.RoleExecutor, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline}, Platform: "linux", Arch: "amd64"},
		}},
		Fingerprints: fixedFingerprints{"coordinator": "coordinator-state-v1", "executor": "executor-state-v1"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	state, err := composition.Trigger(context.Background(), "manual-release")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if state.FlowID != "manual-release" {
		t.Fatalf("state.FlowID = %q", state.FlowID)
	}
	if got, want := service.starts, []updateflow.Request{{FlowID: "manual-release", Targets: []updateflow.Target{
		{Node: update.Node{ID: "executor", Role: update.RoleExecutor, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline}, Platform: "linux", Arch: "amd64", PriorState: "executor-state-v1"},
		{Node: update.Node{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline}, Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1"},
	}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("started requests = %#v, want %#v", got, want)
	}
}

func TestCompositionReopenUsesDurableFlowWithoutCreatingAnotherRelease(t *testing.T) {
	t.Parallel()
	service := &recordingFlowService{runResult: updateflow.State{FlowID: "interrupted", Phase: updateflow.PhaseStopped}}
	composition, err := updatecomposition.Open(updatecomposition.Config{
		Service: service, Targets: updatecomposition.StaticTargetSource{Values: []updatecomposition.Target{{
			Node: update.Node{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline}, Platform: "linux", Arch: "amd64",
		}}}, Fingerprints: fixedFingerprints{"coordinator": "state-v1"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := composition.Reopen(context.Background(), "interrupted")
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if got.FlowID != "interrupted" || len(service.starts) != 0 || !reflect.DeepEqual(service.runs, []string{"interrupted"}) {
		t.Fatalf("reopen result = %#v starts = %#v runs = %#v", got, service.starts, service.runs)
	}
}

func TestCompositionScheduledTriggerIsExplicitAndNeverOverlaps(t *testing.T) {
	t.Parallel()
	service := &blockingFlowService{entered: make(chan struct{}, 2), release: make(chan struct{})}
	composition, err := updatecomposition.Open(updatecomposition.Config{
		Service: service,
		Targets: updatecomposition.StaticTargetSource{Values: []updatecomposition.Target{{
			Node: update.Node{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline}, Platform: "linux", Arch: "amd64",
		}}},
		Fingerprints: fixedFingerprints{"coordinator": "state-v1"},
		Schedule:     updatecomposition.Schedule{Interval: time.Hour},
		FlowIDs:      &sequenceFlowIDs{ids: []string{"scheduled-1", "scheduled-2"}},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := composition.TriggerScheduled(context.Background()); err != nil {
		t.Fatalf("first TriggerScheduled: %v", err)
	}
	select {
	case <-service.entered:
	case <-time.After(time.Second):
		t.Fatal("scheduled trigger did not start")
	}
	if _, err := composition.TriggerScheduled(context.Background()); !errors.Is(err, updatecomposition.ErrTriggerRunning) {
		t.Fatalf("overlapping TriggerScheduled error = %v, want ErrTriggerRunning", err)
	}
	close(service.release)
	if _, err := composition.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got, want := service.starts[0].FlowID, "scheduled-1"; got != want {
		t.Fatalf("scheduled flow id = %q, want %q", got, want)
	}
}

type fixedFingerprints map[string]string

type fixedInstalledState struct{ state updateinstall.InstalledState }

func (s fixedInstalledState) ReadInstalledState(context.Context, string) (updateinstall.InstalledState, error) {
	return s.state, nil
}

func (f fixedFingerprints) StateFingerprint(_ context.Context, nodeID string) (string, error) {
	value, ok := f[nodeID]
	if !ok {
		return "", errors.New("absent fingerprint")
	}
	return value, nil
}

type recordingFlowService struct {
	starts    []updateflow.Request
	runs      []string
	runResult updateflow.State
}

func (s *recordingFlowService) Start(_ context.Context, request updateflow.Request) (updateflow.State, error) {
	s.starts = append(s.starts, request)
	return updateflow.State{FlowID: request.FlowID}, nil
}

func (s *recordingFlowService) Run(_ context.Context, flowID string) (updateflow.State, error) {
	s.runs = append(s.runs, flowID)
	return s.runResult, nil
}

type sequenceFlowIDs struct {
	ids []string
	mu  sync.Mutex
}

func (s *sequenceFlowIDs) NextFlowID(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ids) == 0 {
		return "", errors.New("no flow IDs")
	}
	result := s.ids[0]
	s.ids = s.ids[1:]
	return result, nil
}

type blockingFlowService struct {
	starts  []updateflow.Request
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
}

func (s *blockingFlowService) Start(_ context.Context, request updateflow.Request) (updateflow.State, error) {
	s.mu.Lock()
	s.starts = append(s.starts, request)
	s.mu.Unlock()
	s.entered <- struct{}{}
	<-s.release
	return updateflow.State{FlowID: request.FlowID}, nil
}

func (s *blockingFlowService) Run(context.Context, string) (updateflow.State, error) {
	return updateflow.State{}, errors.New("unexpected Run")
}
