package coordinatortransfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bria/internal/computer"
	"bria/internal/domain"
	"bria/internal/nodelink"
)

const maxTransferStateBytes = 16 << 20
const defaultQuiesceTimeout = 30 * time.Second

var (
	ErrInvalidTransfer = errors.New("invalid manual coordinator transfer")
	ErrTransferPhase   = errors.New("manual coordinator transfer is in another phase")
	ErrQuiesceExpired  = errors.New("manual coordinator transfer quiesce expired")
)

type Phase string

const (
	PhasePrepared  Phase = "prepared"
	PhaseStaged    Phase = "staged"
	PhaseCommitted Phase = "committed"
	PhaseActivated Phase = "activated"
	PhaseFinalized Phase = "finalized"
	PhaseAborted   Phase = "aborted"
)

type Request struct {
	ID                string
	TargetID          domain.ComputerID
	TargetFingerprint string
	Snapshot          Snapshot
}

type LiveReceipt struct {
	Term                      computer.CoordinatorTerm
	Identity                  nodelink.ChannelIdentity
	ReadinessID               string
	ProtocolVersion           uint16
	StateDigest               string
	TelegramReady             bool
	StateStorageReady         bool
	TelegramScope             TelegramScope
	CallbackVerificationKeyID string
}

type Offer struct {
	ID                string                   `json:"id"`
	From              computer.CoordinatorTerm `json:"from"`
	To                computer.CoordinatorTerm `json:"to"`
	TargetFingerprint string                   `json:"target_fingerprint"`
	StateDigest       string                   `json:"state_digest"`
	QuiesceUntil      time.Time                `json:"quiesce_until"`
	Snapshot          Snapshot                 `json:"snapshot"`
}

type StageReceipt struct {
	ID          string
	TargetID    domain.ComputerID
	StateDigest string
}

type AbortReceipt struct {
	ID          string
	TargetID    domain.ComputerID
	StateDigest string
	Term        computer.CoordinatorTerm
	Identity    nodelink.ChannelIdentity
}

type Commit struct{ Offer Offer }

type Activation struct {
	ID   string
	Term computer.CoordinatorTerm
}

type PendingTransfer struct {
	Phase Phase
	Offer Offer
}

type transferRecord struct {
	Version uint16 `json:"version"`
	Role    string `json:"role"`
	Phase   Phase  `json:"phase"`
	Offer   Offer  `json:"offer"`
}

type Source struct {
	mu       sync.RWMutex
	path     string
	term     computer.CoordinatorTerm
	pairings *nodelink.PairingFile
	now      func() time.Time
	quiesce  time.Duration
	record   transferRecord
}

func OpenSource(path string, term computer.CoordinatorTerm, pairings *nodelink.PairingFile) (*Source, error) {
	return OpenSourceWithTiming(path, term, pairings, time.Now, defaultQuiesceTimeout)
}

func OpenSourceWithTiming(path string, term computer.CoordinatorTerm, pairings *nodelink.PairingFile, now func() time.Time, quiesce time.Duration) (*Source, error) {
	if pairings == nil || now == nil || quiesce < time.Second || quiesce > time.Minute || quiesce%time.Second != 0 || term.Generation == 0 || strings.TrimSpace(string(term.CoordinatorID)) == "" {
		return nil, ErrInvalidTransfer
	}
	record, err := readRecord(path)
	if err != nil {
		return nil, err
	}
	if record.Role != "" && (record.Role != "source" || record.Offer.From != term) {
		return nil, ErrInvalidTransfer
	}
	return &Source{path: path, term: term, pairings: pairings, now: now, quiesce: quiesce, record: record}, nil
}

func (source *Source) Prepare(ctx context.Context, request Request, live LiveReceipt) (Offer, error) {
	if source == nil || ctx == nil || ctx.Err() != nil || !validID(request.ID) || request.TargetID == source.term.CoordinatorID || !source.pairings.Authorized(request.TargetID, request.TargetFingerprint) {
		return Offer{}, ErrInvalidTransfer
	}
	digest, err := request.Snapshot.Digest()
	if err != nil {
		return Offer{}, err
	}
	identity := live.Identity
	if live.Term != source.term || !validID(live.ReadinessID) || live.ProtocolVersion != nodelink.ProtocolVersion || live.StateDigest != digest || !live.TelegramReady || !live.StateStorageReady || live.TelegramScope != request.Snapshot.TelegramScope || live.CallbackVerificationKeyID != request.Snapshot.CallbackVerificationKeyID || !identity.MutuallyAuthenticated || identity.LocalComputerID != source.term.CoordinatorID || identity.PeerComputerID != request.TargetID || identity.ExecutorComputerID != request.TargetID || identity.PeerCertificateSHA256 != request.TargetFingerprint {
		return Offer{}, ErrInvalidTransfer
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.record.Phase == PhasePrepared {
		offer := source.record.Offer
		if offer.ID == request.ID && offer.To.CoordinatorID == request.TargetID && offer.TargetFingerprint == request.TargetFingerprint && offer.StateDigest == digest && source.now().Before(offer.QuiesceUntil) {
			return cloneOffer(offer), nil
		}
		return Offer{}, ErrTransferPhase
	}
	if source.record.Phase != "" && source.record.Phase != PhaseAborted {
		return Offer{}, ErrTransferPhase
	}
	offer := Offer{ID: request.ID, From: source.term, To: computer.CoordinatorTerm{CoordinatorID: request.TargetID, Generation: source.term.Generation + 1}, TargetFingerprint: request.TargetFingerprint, StateDigest: digest, QuiesceUntil: source.now().UTC().Truncate(time.Second).Add(source.quiesce), Snapshot: cloneSnapshot(request.Snapshot)}
	record := transferRecord{Version: 1, Role: "source", Phase: PhasePrepared, Offer: offer}
	if err := writeRecord(source.path, record); err != nil {
		return Offer{}, err
	}
	source.record = record
	return cloneOffer(offer), nil
}

func (source *Source) Commit(ctx context.Context, receipt StageReceipt) (Commit, error) {
	if source == nil || ctx.Err() != nil {
		return Commit{}, ErrInvalidTransfer
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if receipt.ID != source.record.Offer.ID || receipt.TargetID != source.record.Offer.To.CoordinatorID || receipt.StateDigest != source.record.Offer.StateDigest {
		return Commit{}, ErrTransferPhase
	}
	if source.record.Phase == PhaseCommitted || source.record.Phase == PhaseFinalized {
		return Commit{Offer: cloneOffer(source.record.Offer)}, nil
	}
	if source.record.Phase != PhasePrepared {
		return Commit{}, ErrTransferPhase
	}
	if !source.now().Before(source.record.Offer.QuiesceUntil) {
		return Commit{}, ErrQuiesceExpired
	}
	record := source.record
	record.Phase = PhaseCommitted
	if err := writeRecord(source.path, record); err != nil {
		return Commit{}, err
	}
	source.record = record
	return Commit{Offer: cloneOffer(record.Offer)}, nil
}

// Abort releases the quiesce only after the authenticated target durably
// proves that its exact offer is not activatable and the old fence is intact.
func (source *Source) Abort(ctx context.Context, receipt AbortReceipt, fence *computer.FenceFile) error {
	if source == nil || ctx == nil || ctx.Err() != nil || fence == nil {
		return ErrInvalidTransfer
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	offer := source.record.Offer
	identity := receipt.Identity
	if source.record.Phase != PhasePrepared || receipt.ID != offer.ID || receipt.TargetID != offer.To.CoordinatorID || receipt.StateDigest != offer.StateDigest || receipt.Term != offer.From || !identity.MutuallyAuthenticated || identity.LocalComputerID != offer.From.CoordinatorID || identity.PeerComputerID != offer.To.CoordinatorID || identity.ExecutorComputerID != offer.To.CoordinatorID || identity.PeerCertificateSHA256 != offer.TargetFingerprint || fence.Validate(offer.From) != nil {
		return ErrTransferPhase
	}
	record := source.record
	record.Phase = PhaseAborted
	if err := writeRecord(source.path, record); err != nil {
		return err
	}
	source.record = record
	return nil
}

// ReclaimExpired automatically restores the source after the bounded
// pre-commit window. An expired offer cannot be newly staged or committed.
func (source *Source) ReclaimExpired(ctx context.Context, fence *computer.FenceFile) error {
	if source == nil || ctx == nil || ctx.Err() != nil || fence == nil {
		return ErrInvalidTransfer
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.record.Phase != PhasePrepared || source.now().Before(source.record.Offer.QuiesceUntil) || fence.Validate(source.record.Offer.From) != nil {
		return ErrTransferPhase
	}
	record := source.record
	record.Phase = PhaseAborted
	if err := writeRecord(source.path, record); err != nil {
		return err
	}
	source.record = record
	return nil
}

func (source *Source) Finalize(ctx context.Context, activation Activation) error {
	if source == nil || ctx.Err() != nil {
		return ErrInvalidTransfer
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if activation.ID != source.record.Offer.ID || activation.Term != source.record.Offer.To {
		return ErrTransferPhase
	}
	if source.record.Phase == PhaseFinalized {
		return nil
	}
	if source.record.Phase != PhaseCommitted {
		return ErrTransferPhase
	}
	record := source.record
	record.Phase = PhaseFinalized
	if err := writeRecord(source.path, record); err != nil {
		return err
	}
	source.record = record
	return nil
}

func (source *Source) CanCoordinate() bool {
	if source == nil {
		return false
	}
	source.mu.RLock()
	defer source.mu.RUnlock()
	return source.record.Phase == "" || source.record.Phase == PhaseAborted
}

func (source *Source) Pending() PendingTransfer {
	if source == nil {
		return PendingTransfer{}
	}
	source.mu.RLock()
	defer source.mu.RUnlock()
	return PendingTransfer{Phase: source.record.Phase, Offer: cloneOffer(source.record.Offer)}
}

type Target struct {
	mu          sync.RWMutex
	path        string
	id          domain.ComputerID
	fingerprint string
	fence       *computer.FenceFile
	state       AtomicStateStore
	now         func() time.Time
	record      transferRecord
}

func OpenTarget(path string, id domain.ComputerID, fingerprint string, fence *computer.FenceFile, state AtomicStateStore) (*Target, error) {
	return OpenTargetWithClock(path, id, fingerprint, fence, state, time.Now)
}

func OpenTargetWithClock(path string, id domain.ComputerID, fingerprint string, fence *computer.FenceFile, state AtomicStateStore, now func() time.Time) (*Target, error) {
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(fingerprint) == "" || fence == nil || state == nil || now == nil {
		return nil, ErrInvalidTransfer
	}
	record, err := readRecord(path)
	if err != nil {
		return nil, err
	}
	if record.Role != "" && (record.Role != "target" || record.Offer.To.CoordinatorID != id || record.Offer.TargetFingerprint != fingerprint) {
		return nil, ErrInvalidTransfer
	}
	return &Target{path: path, id: id, fingerprint: fingerprint, fence: fence, state: state, now: now, record: record}, nil
}

func (target *Target) Stage(ctx context.Context, offer Offer) (StageReceipt, error) {
	if target == nil || ctx.Err() != nil || !validOffer(offer) || offer.To.CoordinatorID != target.id || offer.TargetFingerprint != target.fingerprint || offer.To.Generation != offer.From.Generation+1 {
		return StageReceipt{}, ErrInvalidTransfer
	}
	remaining := offer.QuiesceUntil.Sub(target.now())
	if remaining <= 0 || remaining > time.Minute {
		return StageReceipt{}, ErrInvalidTransfer
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.record.Phase == PhaseStaged && sameOffer(target.record.Offer, offer) {
		return StageReceipt{ID: offer.ID, TargetID: target.id, StateDigest: offer.StateDigest}, nil
	}
	if target.record.Phase != "" && target.record.Phase != PhaseAborted {
		return StageReceipt{}, ErrTransferPhase
	}
	if err := target.fence.Validate(offer.From); err != nil {
		return StageReceipt{}, ErrInvalidTransfer
	}
	receipt, err := target.state.Stage(ctx, offer.ID, cloneSnapshot(offer.Snapshot))
	if err != nil || receipt.TransferID != offer.ID || receipt.Digest != offer.StateDigest {
		return StageReceipt{}, ErrInvalidTransfer
	}
	record := transferRecord{Version: 1, Role: "target", Phase: PhaseStaged, Offer: cloneOffer(offer)}
	if err := writeRecord(target.path, record); err != nil {
		return StageReceipt{}, err
	}
	target.record = record
	return StageReceipt{ID: offer.ID, TargetID: target.id, StateDigest: offer.StateDigest}, nil
}

func (target *Target) Activate(ctx context.Context, commit Commit) (Activation, error) {
	if target == nil || ctx.Err() != nil {
		return Activation{}, ErrInvalidTransfer
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if !sameOffer(target.record.Offer, commit.Offer) {
		return Activation{}, ErrTransferPhase
	}
	if target.record.Phase == PhaseActivated {
		if err := target.fence.Validate(target.record.Offer.To); err != nil {
			return Activation{}, ErrInvalidTransfer
		}
		snapshot, receipt, err := target.state.Read(ctx)
		digest, digestErr := snapshot.Digest()
		if err != nil || digestErr != nil || receipt.TransferID != commit.Offer.ID || receipt.Digest != commit.Offer.StateDigest || digest != commit.Offer.StateDigest {
			return Activation{}, ErrInvalidTransfer
		}
		return Activation{ID: target.record.Offer.ID, Term: target.record.Offer.To}, nil
	}
	if target.record.Phase != PhaseStaged {
		return Activation{}, ErrTransferPhase
	}
	if err := target.fence.Validate(commit.Offer.From); err != nil {
		if nextErr := target.fence.Validate(commit.Offer.To); nextErr != nil {
			return Activation{}, ErrInvalidTransfer
		}
	}
	if err := target.state.Apply(ctx, commit.Offer.ID, commit.Offer.StateDigest); err != nil {
		return Activation{}, err
	}
	rereadSnapshot, reread, err := target.state.Read(ctx)
	rereadDigest, digestErr := rereadSnapshot.Digest()
	if err != nil || digestErr != nil || reread.TransferID != commit.Offer.ID || reread.Digest != commit.Offer.StateDigest || rereadDigest != commit.Offer.StateDigest {
		return Activation{}, ErrInvalidTransfer
	}
	if err := target.fence.Accept(commit.Offer.To); err != nil {
		return Activation{}, err
	}
	record := target.record
	record.Phase = PhaseActivated
	if err := writeRecord(target.path, record); err != nil {
		return Activation{}, err
	}
	target.record = record
	return Activation{ID: record.Offer.ID, Term: record.Offer.To}, nil
}

func (target *Target) CanCoordinate() bool {
	if target == nil {
		return false
	}
	target.mu.RLock()
	defer target.mu.RUnlock()
	return target.record.Phase == PhaseActivated
}

func (target *Target) Pending() PendingTransfer {
	if target == nil {
		return PendingTransfer{}
	}
	target.mu.RLock()
	defer target.mu.RUnlock()
	return PendingTransfer{Phase: target.record.Phase, Offer: cloneOffer(target.record.Offer)}
}

// Abort durably makes an uncommitted staged offer non-activatable. The receipt
// can be returned over the same authenticated channel to release the source.
func (target *Target) Abort(ctx context.Context, offer Offer, identity nodelink.ChannelIdentity) (AbortReceipt, error) {
	if target == nil || ctx == nil || ctx.Err() != nil || !validOffer(offer) || offer.To.CoordinatorID != target.id || offer.TargetFingerprint != target.fingerprint || !identity.MutuallyAuthenticated || identity.LocalComputerID != offer.From.CoordinatorID || identity.PeerComputerID != target.id || identity.ExecutorComputerID != target.id || identity.PeerCertificateSHA256 != target.fingerprint {
		return AbortReceipt{}, ErrInvalidTransfer
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if err := target.fence.Validate(offer.From); err != nil {
		return AbortReceipt{}, ErrTransferPhase
	}
	if target.record.Phase == PhaseAborted && sameOffer(target.record.Offer, offer) {
		return AbortReceipt{ID: offer.ID, TargetID: target.id, StateDigest: offer.StateDigest, Term: offer.From, Identity: identity}, nil
	}
	if target.record.Phase != "" && (target.record.Phase != PhaseStaged || !sameOffer(target.record.Offer, offer)) {
		return AbortReceipt{}, ErrTransferPhase
	}
	record := transferRecord{Version: 1, Role: "target", Phase: PhaseAborted, Offer: cloneOffer(offer)}
	if err := writeRecord(target.path, record); err != nil {
		return AbortReceipt{}, err
	}
	target.record = record
	return AbortReceipt{ID: offer.ID, TargetID: target.id, StateDigest: offer.StateDigest, Term: offer.From, Identity: identity}, nil
}

func validOffer(offer Offer) bool {
	if !validID(offer.ID) || offer.From.Generation == 0 || offer.To.Generation == 0 || offer.From.CoordinatorID == offer.To.CoordinatorID || offer.QuiesceUntil.IsZero() || offer.QuiesceUntil.Nanosecond() != 0 {
		return false
	}
	digest, err := offer.Snapshot.Digest()
	return err == nil && offer.StateDigest == digest && strings.TrimSpace(offer.TargetFingerprint) != ""
}

func validID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, character := range id {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func sameOffer(left, right Offer) bool {
	return left.ID == right.ID && left.From == right.From && left.To == right.To && left.TargetFingerprint == right.TargetFingerprint && left.StateDigest == right.StateDigest && left.QuiesceUntil.Equal(right.QuiesceUntil)
}

func cloneOffer(offer Offer) Offer {
	offer.Snapshot = cloneSnapshot(offer.Snapshot)
	return offer
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	encoded, _ := json.Marshal(snapshot)
	var clone Snapshot
	_ = json.Unmarshal(encoded, &clone)
	return clone
}

func readRecord(path string) (transferRecord, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return transferRecord{}, nil
	}
	if err != nil || len(data) > maxTransferStateBytes+4096 {
		return transferRecord{}, ErrInvalidTransfer
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record transferRecord
	if err := decoder.Decode(&record); err != nil {
		return transferRecord{}, ErrInvalidTransfer
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || record.Version != 1 || !validOffer(record.Offer) {
		return transferRecord{}, ErrInvalidTransfer
	}
	return record, nil
}

func writeRecord(path string, record transferRecord) error {
	if !validOffer(record.Offer) {
		return ErrInvalidTransfer
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxTransferStateBytes+4096 {
		return ErrInvalidTransfer
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".transfer-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_ = temporary.Chmod(0o600)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	if err := directory.Close(); err != nil {
		return err
	}
	verified, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, verified) {
		return ErrInvalidTransfer
	}
	return nil
}
