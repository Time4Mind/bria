// Package updateflow composes verified releases, durable rollout state, and
// injected platform operations without owning a network or installation
// mechanism.
package updateflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"bria/internal/update"
)

const StateFormatVersion = 1

var (
	ErrInvalidService   = errors.New("invalid update flow service")
	ErrInvalidRequest   = errors.New("invalid update flow request")
	ErrFlowAbsent       = errors.New("update flow does not exist")
	ErrFlowExists       = errors.New("update flow already exists")
	ErrRevisionConflict = errors.New("update flow state revision conflict")
	ErrReceiptMismatch  = errors.New("update operation receipt does not match request")
	ErrArtifactUnstaged = errors.New("release artifact was not completely staged")
	ErrStageUnknown     = errors.New("release artifact staging outcome is unknown")
	ErrRollbackUnknown  = errors.New("release rollback outcome is unknown")
	ErrUpdateRolledBack = errors.New("update failed and prior version was restored")
)

var flowLocks sync.Map

type ArtifactPayload interface {
	io.Reader
	io.Seeker
	io.Closer
}

type Source interface {
	SignedManifest(context.Context) ([]byte, error)
	Artifact(context.Context, update.Artifact) (ArtifactPayload, error)
}

type StageRequest struct {
	OperationID    string
	NodeID         string
	Version        string
	SignedManifest []byte
	Artifact       update.Artifact
	Content        io.Reader
}

type StageReceipt struct {
	OperationID string          `json:"operation_id"`
	NodeID      string          `json:"node_id"`
	Version     string          `json:"version"`
	Artifact    update.Artifact `json:"artifact"`
	Reference   string          `json:"reference"`
}

type StageStatus string

const (
	StageUnconfirmed StageStatus = "unconfirmed"
	StageConfirmed   StageStatus = "confirmed"
)

type StageOperation struct {
	OperationID string          `json:"operation_id"`
	NodeID      string          `json:"node_id"`
	Version     string          `json:"version"`
	Artifact    update.Artifact `json:"artifact"`
	Status      StageStatus     `json:"status"`
	Receipt     *StageReceipt   `json:"receipt,omitempty"`
}

type Stager interface {
	// Implementations must treat OperationID idempotently and return the same
	// durable stage reference when replayed after a process crash.
	Stage(context.Context, StageRequest) (StageReceipt, error)
}

type InstallRequest struct {
	OperationID   string
	NodeID        string
	FromVersion   string
	TargetVersion string
	PriorState    string
	Stage         StageReceipt
}

type RollbackRequest struct {
	OperationID   string
	NodeID        string
	FromVersion   string
	TargetVersion string
	TargetState   string
}

type InstallReceipt struct {
	OperationID    string
	NodeID         string
	RunningVersion string
	State          string
	Applied        bool
}

type Installer interface {
	// Implementations must treat OperationID idempotently and return the same
	// physical receipt when an operation is replayed after a process crash.
	Install(context.Context, InstallRequest) (InstallReceipt, error)
	Rollback(context.Context, RollbackRequest) (InstallReceipt, error)
}

type PostflightReceipt struct {
	Health           update.HealthReceipt
	StateFingerprint string
}

type Postflight interface {
	Probe(context.Context, string, string) (PostflightReceipt, error)
}

type Store interface {
	Load(context.Context, string) (State, bool, error)
	Save(context.Context, State) error
}

type Target struct {
	Node       update.Node `json:"node"`
	Platform   string      `json:"platform"`
	Arch       string      `json:"arch"`
	PriorState string      `json:"prior_state"`
}

type Request struct {
	FlowID  string
	Targets []Target
}

type Phase string

const (
	PhasePrepared               Phase = "prepared"
	PhaseInstalling             Phase = "installing"
	PhaseAwaitingPostflight     Phase = "awaiting_postflight"
	PhaseRollingBack            Phase = "rolling_back"
	PhaseAwaitingRollbackHealth Phase = "awaiting_rollback_postflight"
	PhaseStopped                Phase = "stopped"
	PhaseCompleted              Phase = "completed"
	PhaseRollbackFailed         Phase = "rollback_failed"
)

type State struct {
	FormatVersion   int                       `json:"format_version"`
	Revision        uint64                    `json:"revision"`
	FlowID          string                    `json:"flow_id"`
	SignedManifest  []byte                    `json:"signed_manifest"`
	VerifiedKeyID   string                    `json:"verified_key_id"`
	Manifest        update.Manifest           `json:"manifest"`
	Rollout         update.Rollout            `json:"rollout"`
	Targets         []Target                  `json:"targets"`
	StageOperations map[string]StageOperation `json:"stage_operations"`
	Phase           Phase                     `json:"phase"`
}

type Service struct {
	Source      Source
	Stager      Stager
	Installer   Installer
	Postflight  Postflight
	Store       Store
	TrustedKeys update.TrustedKeys
}

func (s Service) Prepare(ctx context.Context, request Request) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	lock := lockFor(request.FlowID)
	lock.Lock()
	defer lock.Unlock()
	return s.prepareLocked(ctx, request)
}

func (s Service) Advance(ctx context.Context, flowID string) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	lock := lockFor(flowID)
	lock.Lock()
	defer lock.Unlock()
	state, err := s.loadVerified(ctx, flowID)
	if err != nil {
		return State{}, err
	}
	err = s.advanceCurrent(ctx, &state)
	return state, err
}

func (s Service) Run(ctx context.Context, flowID string) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	lock := lockFor(flowID)
	lock.Lock()
	defer lock.Unlock()
	state, err := s.loadVerified(ctx, flowID)
	if err != nil {
		return State{}, err
	}
	return s.runLocked(ctx, state)
}

// Resume is an explicit recovery from an availability stop. The caller must
// provide a fresh online observation for the currently blocked node.
func (s Service) Resume(ctx context.Context, flowID string, availability update.Availability) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	lock := lockFor(flowID)
	lock.Lock()
	defer lock.Unlock()
	state, err := s.loadVerified(ctx, flowID)
	if err != nil {
		return State{}, err
	}
	if state.Rollout.CurrentIndex < 0 || state.Rollout.CurrentIndex >= len(state.Rollout.OrderedNodes) {
		return state, update.ErrUnexpectedState
	}
	nodeID := state.Rollout.OrderedNodes[state.Rollout.CurrentIndex].ID
	if err := state.Rollout.SetAvailability(nodeID, availability); err != nil {
		return state, err
	}
	if err := state.Rollout.Resume(); err != nil {
		return state, err
	}
	state.Phase = derivePhase(state)
	if err := s.persist(ctx, &state); err != nil {
		return state, err
	}
	return s.runLocked(ctx, state)
}

// RetryStage is the only operation that may repeat an unconfirmed staging
// attempt. It reuses the durable operation identity.
func (s Service) RetryStage(ctx context.Context, flowID string) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	lock := lockFor(flowID)
	lock.Lock()
	defer lock.Unlock()
	state, err := s.loadVerified(ctx, flowID)
	if err != nil {
		return State{}, err
	}
	if state.Rollout.Status() != update.RolloutRunning || state.Rollout.CurrentIndex < 0 ||
		state.Rollout.CurrentIndex >= len(state.Targets) {
		return state, update.ErrUnexpectedState
	}
	target := state.Targets[state.Rollout.CurrentIndex]
	node := state.Rollout.OrderedNodes[state.Rollout.CurrentIndex]
	operation, found := state.StageOperations[node.ID]
	if node.State != update.NodePending || !found || operation.Status != StageUnconfirmed || operation.Receipt != nil {
		return state, update.ErrUnexpectedState
	}
	payload, err := s.Source.Artifact(ctx, operation.Artifact)
	if err != nil {
		return state, err
	}
	if payload == nil {
		return state, ErrArtifactUnstaged
	}
	if err := update.VerifyArtifact(payload, operation.Artifact); err != nil {
		_ = payload.Close()
		return state, err
	}
	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		_ = payload.Close()
		return state, err
	}
	receipt, stageErr := s.stagePayload(ctx, target, operation, state.SignedManifest, payload)
	closeErr := payload.Close()
	if stageErr != nil || closeErr != nil {
		return state, ErrStageUnknown
	}
	operation.Status = StageConfirmed
	operation.Receipt = &receipt
	state.StageOperations[node.ID] = operation
	if err := s.persist(ctx, &state); err != nil {
		return state, err
	}
	return state, nil
}

// RetryRollback is the only operation that may repeat an unconfirmed rollback.
// It reuses the durable operation identity held by NodeRollingBack.
func (s Service) RetryRollback(ctx context.Context, flowID string) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	lock := lockFor(flowID)
	lock.Lock()
	defer lock.Unlock()
	state, err := s.loadVerified(ctx, flowID)
	if err != nil {
		return State{}, err
	}
	if state.Rollout.Status() != update.RolloutRunning || state.Rollout.CurrentIndex < 0 ||
		state.Rollout.CurrentIndex >= len(state.Targets) {
		return state, update.ErrUnexpectedState
	}
	target := state.Targets[state.Rollout.CurrentIndex]
	node := state.Rollout.OrderedNodes[state.Rollout.CurrentIndex]
	if node.State != update.NodeRollingBack {
		return state, update.ErrUnexpectedState
	}
	request := newRollbackRequest(state, target, node)
	receipt, rollbackErr := s.Installer.Rollback(ctx, request)
	if rollbackErr != nil || !validRollbackReceipt(receipt, request) {
		return state, ErrRollbackUnknown
	}
	if err := state.Rollout.RecordApplied(node.ID); err != nil {
		return state, err
	}
	state.Phase = derivePhase(state)
	if err := s.persist(ctx, &state); err != nil {
		return state, err
	}
	return state, nil
}

func (s Service) Start(ctx context.Context, request Request) (State, error) {
	if err := s.validate(); err != nil {
		return State{}, err
	}
	lock := lockFor(request.FlowID)
	lock.Lock()
	defer lock.Unlock()
	state, err := s.prepareLocked(ctx, request)
	if err != nil {
		return State{}, err
	}
	return s.runLocked(ctx, state)
}

func (s Service) prepareLocked(ctx context.Context, request Request) (State, error) {
	if invalidIdentity(request.FlowID, 1024) || len(request.Targets) == 0 {
		return State{}, ErrInvalidRequest
	}
	if _, found, err := s.Store.Load(ctx, request.FlowID); err != nil {
		return State{}, err
	} else if found {
		return State{}, ErrFlowExists
	}
	signed, err := s.Source.SignedManifest(ctx)
	if err != nil {
		return State{}, fmt.Errorf("load signed release manifest: %w", err)
	}
	manifest, keyID, err := update.VerifySignedManifest(signed, s.TrustedKeys)
	if err != nil {
		return State{}, err
	}
	nodes := make([]update.Node, 0, len(request.Targets))
	targetsByID := make(map[string]Target, len(request.Targets))
	for _, target := range request.Targets {
		if invalidIdentity(target.Node.ID, 1024) || invalidIdentity(target.Platform, 64) ||
			invalidIdentity(target.Arch, 64) || invalidIdentity(target.PriorState, 4096) {
			return State{}, ErrInvalidRequest
		}
		if _, duplicate := targetsByID[target.Node.ID]; duplicate {
			return State{}, ErrInvalidRequest
		}
		if _, err := update.SelectArtifact(manifest, target.Platform, target.Arch); err != nil {
			return State{}, err
		}
		targetsByID[target.Node.ID] = target
		nodes = append(nodes, target.Node)
	}
	rollout, err := update.NewRollout(manifest.Version, nodes)
	if err != nil {
		return State{}, err
	}
	orderedTargets := make([]Target, 0, len(request.Targets))
	for _, node := range rollout.Nodes() {
		orderedTargets = append(orderedTargets, targetsByID[node.ID])
	}
	state := State{
		FormatVersion: StateFormatVersion, FlowID: request.FlowID,
		SignedManifest: append([]byte(nil), signed...), VerifiedKeyID: keyID, Manifest: manifest,
		Rollout: *rollout, Targets: orderedTargets,
		StageOperations: make(map[string]StageOperation), Phase: PhasePrepared,
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	if err := s.persist(ctx, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s Service) runLocked(ctx context.Context, state State) (State, error) {
	for state.Rollout.Status() == update.RolloutRunning {
		if err := s.advanceCurrent(ctx, &state); err != nil {
			return state, err
		}
	}
	return state, nil
}

func (s Service) advanceCurrent(ctx context.Context, state *State) error {
	if state.Rollout.Status() != update.RolloutRunning || state.Rollout.CurrentIndex < 0 ||
		state.Rollout.CurrentIndex >= len(state.Targets) {
		return update.ErrUnexpectedState
	}
	target := state.Targets[state.Rollout.CurrentIndex]
	rollbackIssued := false
	for {
		node := state.Rollout.OrderedNodes[state.Rollout.CurrentIndex]
		switch node.State {
		case update.NodePending:
			if node.Availability != update.AvailabilityOnline {
				_, err := state.Rollout.NextAction()
				state.Phase = derivePhase(*state)
				if saveErr := s.persist(ctx, state); saveErr != nil {
					return saveErr
				}
				return err
			}
			operation, found := state.StageOperations[node.ID]
			if !found {
				if err := s.beginStage(ctx, state, target); err != nil {
					return err
				}
				operation = state.StageOperations[node.ID]
			}
			if operation.Status != StageConfirmed || operation.Receipt == nil {
				return ErrStageUnknown
			}
			if _, err := state.Rollout.NextAction(); err != nil {
				state.Phase = derivePhase(*state)
				if saveErr := s.persist(ctx, state); saveErr != nil {
					return saveErr
				}
				return err
			}
			state.Phase = derivePhase(*state)
			if err := s.persist(ctx, state); err != nil {
				return err
			}
		case update.NodeRollbackPending:
			if _, err := state.Rollout.NextAction(); err != nil {
				state.Phase = derivePhase(*state)
				if saveErr := s.persist(ctx, state); saveErr != nil {
					return saveErr
				}
				return err
			}
			state.Phase = derivePhase(*state)
			if err := s.persist(ctx, state); err != nil {
				return err
			}
			rollbackIssued = true
		case update.NodeInstalling:
			operation, found := state.StageOperations[node.ID]
			if !found || operation.Status != StageConfirmed || operation.Receipt == nil {
				return ErrStageUnknown
			}
			stage := *operation.Receipt
			request := InstallRequest{
				OperationID: operationID(state.FlowID, node.ID, "install", state.Manifest.Version),
				NodeID:      node.ID, FromVersion: target.Node.CurrentVersion,
				TargetVersion: state.Manifest.Version, PriorState: target.PriorState, Stage: stage,
			}
			receipt, err := s.Installer.Install(ctx, request)
			if err != nil || !validInstallReceipt(receipt, request) {
				if rollbackErr := s.scheduleRollback(ctx, state, node.ID); rollbackErr != nil {
					return rollbackErr
				}
				continue
			}
			if err := state.Rollout.RecordApplied(node.ID); err != nil {
				return err
			}
			state.Phase = derivePhase(*state)
			if err := s.persist(ctx, state); err != nil {
				return err
			}
		case update.NodeAwaitingHealth:
			receipt, probeErr := s.Postflight.Probe(ctx, node.ID, state.Manifest.Version)
			if probeErr != nil || receipt.StateFingerprint != target.PriorState {
				receipt = PostflightReceipt{Health: update.HealthReceipt{NodeID: node.ID, RunningVersion: state.Manifest.Version}}
			}
			recordErr := state.Rollout.RecordHealth(receipt.Health)
			state.Phase = derivePhase(*state)
			if err := s.persist(ctx, state); err != nil {
				return err
			}
			if recordErr != nil {
				continue
			}
			return nil
		case update.NodeRollingBack:
			if !rollbackIssued {
				return ErrRollbackUnknown
			}
			request := newRollbackRequest(*state, target, node)
			receipt, err := s.Installer.Rollback(ctx, request)
			if err != nil || !validRollbackReceipt(receipt, request) {
				return ErrRollbackUnknown
			}
			if err := state.Rollout.RecordApplied(node.ID); err != nil {
				return err
			}
			state.Phase = derivePhase(*state)
			if err := s.persist(ctx, state); err != nil {
				return err
			}
		case update.NodeAwaitingRollbackHealth:
			receipt, probeErr := s.Postflight.Probe(ctx, node.ID, target.Node.CurrentVersion)
			if probeErr != nil || receipt.StateFingerprint != target.PriorState {
				receipt = PostflightReceipt{Health: update.HealthReceipt{NodeID: node.ID, RunningVersion: target.Node.CurrentVersion}}
			}
			recordErr := state.Rollout.RecordHealth(receipt.Health)
			state.Phase = derivePhase(*state)
			if err := s.persist(ctx, state); err != nil {
				return err
			}
			if recordErr != nil {
				return update.ErrRollbackFailed
			}
			return ErrUpdateRolledBack
		default:
			return update.ErrUnexpectedState
		}
	}
}

func (s Service) scheduleRollback(ctx context.Context, state *State, nodeID string) error {
	if err := state.Rollout.RecordApplyFailure(nodeID); err != nil {
		return err
	}
	state.Phase = derivePhase(*state)
	return s.persist(ctx, state)
}

func (s Service) beginStage(ctx context.Context, state *State, target Target) error {
	artifact, err := update.SelectArtifact(state.Manifest, target.Platform, target.Arch)
	if err != nil {
		return err
	}
	payload, err := s.Source.Artifact(ctx, artifact)
	if err != nil {
		return err
	}
	if payload == nil {
		return ErrArtifactUnstaged
	}
	if err := update.VerifyArtifact(payload, artifact); err != nil {
		_ = payload.Close()
		return err
	}
	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		_ = payload.Close()
		return err
	}
	stageOperation := stageOperationID(state.FlowID, target.Node.ID, state.Manifest.Version, artifact)
	operation := StageOperation{
		OperationID: stageOperation, NodeID: target.Node.ID, Version: state.Manifest.Version,
		Artifact: artifact, Status: StageUnconfirmed,
	}
	state.StageOperations[target.Node.ID] = operation
	if err := s.persist(ctx, state); err != nil {
		_ = payload.Close()
		return err
	}
	receipt, err := s.stagePayload(ctx, target, operation, state.SignedManifest, payload)
	closeErr := payload.Close()
	if err != nil || closeErr != nil {
		return ErrStageUnknown
	}
	operation.Status = StageConfirmed
	operation.Receipt = &receipt
	state.StageOperations[target.Node.ID] = operation
	return s.persist(ctx, state)
}

func (s Service) stagePayload(ctx context.Context, target Target, operation StageOperation, signedManifest []byte, payload ArtifactPayload) (StageReceipt, error) {
	observed := newObservedReader(payload, operation.Artifact.Size)
	receipt, err := s.Stager.Stage(ctx, StageRequest{
		OperationID: operation.OperationID, NodeID: target.Node.ID,
		Version: operation.Version, SignedManifest: append([]byte(nil), signedManifest...),
		Artifact: operation.Artifact, Content: observed,
	})
	if err != nil {
		return StageReceipt{}, err
	}
	if !observed.complete(operation.Artifact) || receipt.OperationID != operation.OperationID || receipt.NodeID != target.Node.ID || receipt.Version != operation.Version ||
		!reflect.DeepEqual(receipt.Artifact, operation.Artifact) || invalidIdentity(receipt.Reference, 4096) {
		return StageReceipt{}, ErrArtifactUnstaged
	}
	return receipt, nil
}

func (s Service) loadVerified(ctx context.Context, flowID string) (State, error) {
	if invalidIdentity(flowID, 1024) {
		return State{}, ErrInvalidRequest
	}
	state, found, err := s.Store.Load(ctx, flowID)
	if err != nil {
		return State{}, err
	}
	if !found {
		return State{}, ErrFlowAbsent
	}
	manifest, keyID, err := update.VerifySignedManifest(state.SignedManifest, s.TrustedKeys)
	if err != nil {
		return State{}, err
	}
	if keyID != state.VerifiedKeyID || !reflect.DeepEqual(manifest, state.Manifest) {
		return State{}, ErrInvalidRequest
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s Service) validate() error {
	if s.Source == nil || s.Stager == nil || s.Installer == nil || s.Postflight == nil || s.Store == nil || len(s.TrustedKeys) == 0 {
		return ErrInvalidService
	}
	return nil
}

func (s Service) persist(ctx context.Context, state *State) error {
	if state == nil || state.Revision == ^uint64(0) {
		return ErrRevisionConflict
	}
	previous := state.Revision
	state.Revision++
	if err := s.Store.Save(ctx, *state); err != nil {
		state.Revision = previous
		return err
	}
	return nil
}

type observedReader struct {
	reader    io.Reader
	remaining int64
	read      int64
	hash      hash.Hash
	sawEOF    bool
}

func newObservedReader(reader io.Reader, size int64) *observedReader {
	return &observedReader{reader: reader, remaining: size, hash: sha256.New()}
}

func (r *observedReader) Read(destination []byte) (int, error) {
	if r.remaining == 0 {
		r.sawEOF = true
		return 0, io.EOF
	}
	if int64(len(destination)) > r.remaining {
		destination = destination[:r.remaining]
	}
	read, err := r.reader.Read(destination)
	if read > 0 {
		_, _ = r.hash.Write(destination[:read])
		r.read += int64(read)
		r.remaining -= int64(read)
	}
	if err == io.EOF {
		r.sawEOF = true
	}
	return read, err
}

func (r *observedReader) complete(artifact update.Artifact) bool {
	return r.sawEOF && r.read == artifact.Size && hex.EncodeToString(r.hash.Sum(nil)) == artifact.SHA256
}

func validInstallReceipt(receipt InstallReceipt, request InstallRequest) bool {
	return receipt.Applied && receipt.OperationID == request.OperationID && receipt.NodeID == request.NodeID &&
		receipt.RunningVersion == request.TargetVersion && receipt.State == request.PriorState
}

func validRollbackReceipt(receipt InstallReceipt, request RollbackRequest) bool {
	return receipt.Applied && receipt.OperationID == request.OperationID && receipt.NodeID == request.NodeID &&
		receipt.RunningVersion == request.TargetVersion && receipt.State == request.TargetState
}

func operationID(flowID, nodeID, kind, version string) string {
	digest := sha256.Sum256([]byte(flowID + "\x00" + nodeID + "\x00" + kind + "\x00" + version))
	return "update:" + kind + ":" + hex.EncodeToString(digest[:16]) + ":" + strconv.Itoa(StateFormatVersion)
}

func newRollbackRequest(state State, target Target, node update.Node) RollbackRequest {
	return RollbackRequest{
		OperationID: operationID(state.FlowID, node.ID, "rollback", target.Node.CurrentVersion),
		NodeID:      node.ID, FromVersion: state.Manifest.Version,
		TargetVersion: target.Node.CurrentVersion, TargetState: target.PriorState,
	}
}

func stageOperationID(flowID, nodeID, version string, artifact update.Artifact) string {
	digest := sha256.Sum256([]byte(flowID + "\x00" + nodeID + "\x00stage\x00" + version + "\x00" +
		artifact.Platform + "\x00" + artifact.Arch + "\x00" + artifact.Name + "\x00" + artifact.SHA256 + "\x00" + strconv.FormatInt(artifact.Size, 10)))
	return "update:stage:" + hex.EncodeToString(digest[:16]) + ":" + strconv.Itoa(StateFormatVersion)
}

func lockFor(flowID string) *sync.Mutex {
	value, _ := flowLocks.LoadOrStore(flowID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func invalidIdentity(value string, max int) bool {
	return value == "" || len(value) > max || value != strings.TrimSpace(value) || strings.ContainsRune(value, 0)
}

func derivePhase(state State) Phase {
	switch state.Rollout.Status() {
	case update.RolloutStopped:
		return PhaseStopped
	case update.RolloutCompleted:
		return PhaseCompleted
	case update.RolloutRollbackFailed:
		return PhaseRollbackFailed
	}
	if state.Rollout.CurrentIndex < 0 || state.Rollout.CurrentIndex >= len(state.Rollout.OrderedNodes) {
		return PhaseStopped
	}
	switch state.Rollout.OrderedNodes[state.Rollout.CurrentIndex].State {
	case update.NodePending:
		return PhasePrepared
	case update.NodeInstalling:
		return PhaseInstalling
	case update.NodeAwaitingHealth:
		return PhaseAwaitingPostflight
	case update.NodeRollbackPending, update.NodeRollingBack:
		return PhaseRollingBack
	case update.NodeAwaitingRollbackHealth:
		return PhaseAwaitingRollbackHealth
	default:
		return PhaseStopped
	}
}

func validateState(state State) error {
	if state.FormatVersion != StateFormatVersion || invalidIdentity(state.FlowID, 1024) ||
		len(state.SignedManifest) == 0 || invalidIdentity(state.VerifiedKeyID, 128) ||
		state.Manifest.Version != state.Rollout.TargetVersion || len(state.Targets) != len(state.Rollout.OrderedNodes) ||
		state.Phase != derivePhase(state) {
		return ErrInvalidRequest
	}
	if err := validateRolloutState(state); err != nil {
		return err
	}
	for index, target := range state.Targets {
		node := state.Rollout.OrderedNodes[index]
		if target.Node.ID != node.ID || target.Node.Role != node.Role || invalidIdentity(target.Platform, 64) ||
			invalidIdentity(target.Arch, 64) || invalidIdentity(target.PriorState, 4096) {
			return ErrInvalidRequest
		}
		if _, err := update.SelectArtifact(state.Manifest, target.Platform, target.Arch); err != nil {
			return ErrInvalidRequest
		}
	}
	if state.StageOperations == nil {
		return ErrInvalidRequest
	}
	for nodeID, operation := range state.StageOperations {
		if nodeID != operation.NodeID || invalidIdentity(operation.OperationID, 256) || operation.Version != state.Manifest.Version {
			return ErrInvalidRequest
		}
		found := false
		for _, target := range state.Targets {
			if target.Node.ID == nodeID {
				artifact, err := update.SelectArtifact(state.Manifest, target.Platform, target.Arch)
				found = err == nil && reflect.DeepEqual(artifact, operation.Artifact) &&
					operation.OperationID == stageOperationID(state.FlowID, nodeID, state.Manifest.Version, artifact)
				break
			}
		}
		if !found {
			return ErrInvalidRequest
		}
		switch operation.Status {
		case StageUnconfirmed:
			if operation.Receipt != nil {
				return ErrInvalidRequest
			}
		case StageConfirmed:
			if operation.Receipt == nil || operation.Receipt.OperationID != operation.OperationID ||
				operation.Receipt.NodeID != operation.NodeID || operation.Receipt.Version != operation.Version ||
				!reflect.DeepEqual(operation.Receipt.Artifact, operation.Artifact) || invalidIdentity(operation.Receipt.Reference, 4096) {
				return ErrInvalidRequest
			}
		default:
			return ErrInvalidRequest
		}
	}
	return nil
}

func validateRolloutState(state State) error {
	nodes := state.Rollout.OrderedNodes
	if len(nodes) == 0 || state.Rollout.CurrentIndex < 0 || state.Rollout.CurrentIndex > len(nodes) {
		return ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(nodes))
	coordinators := 0
	for index, node := range nodes {
		if invalidIdentity(node.ID, 1024) || invalidIdentity(node.CurrentVersion, 128) {
			return ErrInvalidRequest
		}
		if _, duplicate := seen[node.ID]; duplicate {
			return ErrInvalidRequest
		}
		seen[node.ID] = struct{}{}
		switch node.Role {
		case update.RoleExecutor:
			if coordinators > 0 {
				return ErrInvalidRequest
			}
		case update.RoleCoordinator:
			coordinators++
			if index != len(nodes)-1 {
				return ErrInvalidRequest
			}
		default:
			return ErrInvalidRequest
		}
		switch node.Availability {
		case update.AvailabilityUnknown, update.AvailabilityOffline, update.AvailabilityOnline:
		default:
			return ErrInvalidRequest
		}
		switch node.State {
		case update.NodePending, update.NodeInstalling, update.NodeAwaitingHealth,
			update.NodeRollbackPending, update.NodeRollingBack, update.NodeAwaitingRollbackHealth,
			update.NodeUpdated, update.NodeRolledBack, update.NodeBlocked, update.NodeRollbackFailed:
		default:
			return ErrInvalidRequest
		}
		if node.State == update.NodeBlocked {
			if node.BlockedFrom != update.NodePending && node.BlockedFrom != update.NodeRollbackPending {
				return ErrInvalidRequest
			}
		} else if node.BlockedFrom != "" {
			return ErrInvalidRequest
		}
		target := state.Targets[index]
		if invalidIdentity(target.Node.CurrentVersion, 128) ||
			(target.Node.State != "" && target.Node.State != update.NodePending) || target.Node.BlockedFrom != "" {
			return ErrInvalidRequest
		}
		if node.State == update.NodeUpdated {
			if node.CurrentVersion != state.Manifest.Version {
				return ErrInvalidRequest
			}
		} else if node.CurrentVersion != target.Node.CurrentVersion {
			return ErrInvalidRequest
		}
	}
	if coordinators != 1 {
		return ErrInvalidRequest
	}
	current := state.Rollout.CurrentIndex
	switch state.Rollout.Status() {
	case update.RolloutRunning:
		if current >= len(nodes) {
			return ErrInvalidRequest
		}
		for index, node := range nodes {
			if index < current && node.State != update.NodeUpdated {
				return ErrInvalidRequest
			}
			if index > current && node.State != update.NodePending {
				return ErrInvalidRequest
			}
		}
		currentState := nodes[current].State
		if currentState != update.NodePending && currentState != update.NodeInstalling &&
			currentState != update.NodeAwaitingHealth && currentState != update.NodeRollbackPending &&
			currentState != update.NodeRollingBack && currentState != update.NodeAwaitingRollbackHealth {
			return ErrInvalidRequest
		}
	case update.RolloutCompleted:
		if current != len(nodes) {
			return ErrInvalidRequest
		}
		for _, node := range nodes {
			if node.State != update.NodeUpdated {
				return ErrInvalidRequest
			}
		}
	case update.RolloutStopped:
		if current >= len(nodes) || (nodes[current].State != update.NodeBlocked && nodes[current].State != update.NodeRolledBack) {
			return ErrInvalidRequest
		}
		for index, node := range nodes {
			if index < current && node.State != update.NodeUpdated {
				return ErrInvalidRequest
			}
			if index > current && node.State != update.NodePending {
				return ErrInvalidRequest
			}
		}
	case update.RolloutRollbackFailed:
		if current >= len(nodes) || nodes[current].State != update.NodeRollbackFailed {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}
