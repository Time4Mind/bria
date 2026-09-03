// Package backupcomposition wires the bounded local backup runtime from
// concrete Bria state sources. It deliberately exposes no automatic schedule
// and never calls an owner-storage port.
package backupcomposition

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"bria/internal/backupflow"
	"bria/internal/backupruntime"
	"bria/internal/backupsource"
	"bria/internal/domain"
)

var (
	ErrInvalidComposition     = errors.New("invalid backup composition")
	ErrPolicyDecisionRequired = errors.New("backup policy decision is required")
)

// AutomaticSchedule is intentionally a closed choice: v1 has only a manual
// trigger until the product owner decides a cadence.
type AutomaticSchedule uint8

const AutomaticScheduleDisabled AutomaticSchedule = 1

// EncryptionPolicy must be chosen explicitly. Encrypted artifacts need an
// agreed protector and recovery procedure, so this composition refuses that
// policy instead of inventing one.
type EncryptionPolicy uint8

const (
	EncryptionUnspecified EncryptionPolicy = iota
	EncryptionDisabled
	EncryptionRequired
)

// OwnerStorage is a future owner-controlled storage boundary. It is a port
// only in v1: New rejects its use and no external write occurs here.
type OwnerStorage interface {
	ReplaceLatest(context.Context, string, string) (backupruntime.ExternalReceipt, error)
}

type Policy struct {
	Schedule   AutomaticSchedule
	Encryption EncryptionPolicy
	OwnerStore OwnerStorage
}

type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

type Sources struct {
	Settings     backupsource.SettingsSource
	Computers    backupsource.ComputerSource
	Sessions     backupsource.SessionSource
	Journal      backupsource.JournalSource
	HistoryRoots map[domain.Provider]string
	HarnessRoot  string
}

// Options names the canonical local state ports. Sources are semantic typed
// readers - raw credential, log, media, output, cache, and project paths are
// not accepted inputs to this composition.
type Options struct {
	ComputerID          string
	WorkDirectory       string
	LatestPath          string
	RestoreCandidateDir string
	LiveDirectory       string
	MarkerPath          string
	Policy              Policy
	Sources             Sources
	Limits              Limits
}

// Runtime exposes only the manual, serialized backup and restore operations.
type Runtime struct {
	barrier   *ReadBarrier
	runner    *backupruntime.Runner
	flow      backupflow.Service
	activator *backupruntime.DirectoryActivator
}

func New(options Options) (*Runtime, error) {
	if err := options.Policy.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.ComputerID) == "" || !absolute(options.WorkDirectory) || !absolute(options.LatestPath) || !absolute(options.RestoreCandidateDir) || !absolute(options.LiveDirectory) || !absolute(options.MarkerPath) {
		return nil, fmt.Errorf("%w: identity and local paths are required", ErrInvalidComposition)
	}
	if options.Limits.MaxFiles < 8 || options.Limits.MaxFileBytes <= 0 || options.Limits.MaxTotalBytes < options.Limits.MaxFileBytes {
		return nil, fmt.Errorf("%w: bounded snapshot limits are required", ErrInvalidComposition)
	}
	state, err := currentState(options.Sources)
	if err != nil {
		return nil, err
	}
	runner, err := backupruntime.NewRunner(backupruntime.RunOptions{
		WorkDirectory: options.WorkDirectory, LatestPath: options.LatestPath, ComputerID: options.ComputerID,
		State: state, Limits: backupruntime.Limits(options.Limits),
	})
	if err != nil {
		return nil, fmt.Errorf("compose local backup runner: %w", err)
	}
	flow := backupflow.Service{
		SourceRoot: options.WorkDirectory, LatestPath: options.LatestPath, RestoreCandidateDir: options.RestoreCandidateDir,
		ComputerID: options.ComputerID, Layout: backupflow.CanonicalSnapshotLayout(),
	}
	target, err := NewCanonicalRootTarget(options.LiveDirectory, backupsource.RestoreLimits(options.Limits), state.barrier)
	if err != nil {
		return nil, fmt.Errorf("compose canonical root restore target: %w", err)
	}
	reopener, err := backupsource.NewReopener(backupsource.ReopenerOptions{
		Target: target,
		Limits: backupsource.RestoreLimits(options.Limits),
	})
	if err != nil {
		return nil, fmt.Errorf("compose typed restore reopener: %w", err)
	}
	activator, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{
		LiveDirectory: options.LiveDirectory, MarkerPath: options.MarkerPath, Reopener: reopener,
	})
	if err != nil {
		return nil, fmt.Errorf("compose atomic restore activator: %w", err)
	}
	return &Runtime{barrier: state.barrier, runner: runner, flow: flow, activator: activator}, nil
}

func (policy Policy) validate() error {
	if policy.Schedule != AutomaticScheduleDisabled {
		return fmt.Errorf("%w: automatic schedule is disabled pending an explicit cadence", ErrPolicyDecisionRequired)
	}
	switch policy.Encryption {
	case EncryptionDisabled:
	case EncryptionUnspecified:
		return fmt.Errorf("%w: encryption must be explicitly disabled or supplied with an approved protector", ErrPolicyDecisionRequired)
	case EncryptionRequired:
		return fmt.Errorf("%w: encrypted backup protector and recovery policy are not configured", ErrPolicyDecisionRequired)
	default:
		return fmt.Errorf("%w: unknown encryption policy", ErrInvalidComposition)
	}
	if policy.OwnerStore != nil {
		return fmt.Errorf("%w: owner storage is a port only and is not enabled", ErrPolicyDecisionRequired)
	}
	return nil
}

func (runtime *Runtime) ReadBarrier() *ReadBarrier {
	if runtime == nil {
		return nil
	}
	return runtime.barrier
}

func (runtime *Runtime) TriggerBackup(ctx context.Context) (backupruntime.Result, error) {
	if runtime == nil || runtime.runner == nil {
		return backupruntime.Result{}, ErrInvalidComposition
	}
	return runtime.runner.RunOnce(ctx)
}

func (runtime *Runtime) PrepareRestore() (backupflow.PreparedRestore, error) {
	if runtime == nil {
		return backupflow.PreparedRestore{}, ErrInvalidComposition
	}
	return runtime.flow.PrepareRestore()
}

func (runtime *Runtime) ActivateRestore(ctx context.Context, prepared backupflow.PreparedRestore) (backupflow.ActivationReceipt, error) {
	if runtime == nil || runtime.activator == nil {
		return backupflow.ActivationReceipt{}, ErrInvalidComposition
	}
	return runtime.flow.ActivateRestore(ctx, prepared, runtime.activator)
}

func (runtime *Runtime) RecoverRestore(ctx context.Context) (backupflow.ActivationReceipt, bool, error) {
	if runtime == nil || runtime.activator == nil {
		return backupflow.ActivationReceipt{}, false, ErrInvalidComposition
	}
	return runtime.activator.Recover(ctx)
}

type composedState struct {
	barrier *ReadBarrier
	state   *backupsource.CurrentState
}

func (state *composedState) BeginSnapshot(ctx context.Context) (backupruntime.SnapshotTransaction, error) {
	if state == nil || state.state == nil {
		return nil, ErrInvalidComposition
	}
	return state.state.BeginSnapshot(ctx)
}

// CanonicalRootTarget is the concrete aggregate restore target for one local
// canonical-state root. DirectoryActivator atomically switches that complete
// root before this target is called. The target holds the writer barrier while
// it rereads every semantic member, so a bad post-switch root is rejected and
// DirectoryActivator restores the previous directory.
type CanonicalRootTarget struct {
	root    string
	limits  backupsource.RestoreLimits
	barrier *ReadBarrier
}

func NewCanonicalRootTarget(root string, limits backupsource.RestoreLimits, barrier *ReadBarrier) (*CanonicalRootTarget, error) {
	if !absolute(root) || barrier == nil || limits.MaxFiles < 8 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes < limits.MaxFileBytes {
		return nil, fmt.Errorf("%w: canonical root, barrier, and bounded restore limits are required", ErrInvalidComposition)
	}
	return &CanonicalRootTarget{root: filepath.Clean(root), limits: limits, barrier: barrier}, nil
}

func (target *CanonicalRootTarget) PrepareRestore(ctx context.Context, restored backupsource.RestoredState) (backupsource.RestoreTransaction, error) {
	if target == nil || ctx == nil || strings.TrimSpace(restored.Fingerprint) == "" {
		return nil, ErrInvalidComposition
	}
	release, err := target.barrier.BeginWrite(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin canonical-root restore barrier: %w", err)
	}
	return &canonicalRootTransaction{target: target, fingerprint: restored.Fingerprint, release: release}, nil
}

type canonicalRootTransaction struct {
	target      *CanonicalRootTarget
	fingerprint string
	release     func() error
	closed      bool
}

func (transaction *canonicalRootTransaction) Validate(context.Context) error {
	if transaction == nil || transaction.target == nil || transaction.closed {
		return ErrInvalidComposition
	}
	_, err := backupsource.ValidateRestoredState(transaction.target.root, transaction.fingerprint, transaction.target.limits)
	if err != nil {
		return fmt.Errorf("reread canonical root before restore receipt: %w", err)
	}
	return nil
}

// Commit is deliberately empty: the enclosing DirectoryActivator has already
// committed all allowlisted members by one directory rename. Validation and
// Reopen decide whether that activation can become visible to the runtime.
func (transaction *canonicalRootTransaction) Commit(context.Context) error {
	if transaction == nil || transaction.closed {
		return ErrInvalidComposition
	}
	return nil
}

func (transaction *canonicalRootTransaction) Reopen(ctx context.Context) (backupsource.RestoreReceipt, error) {
	if transaction == nil || transaction.target == nil || transaction.closed || ctx == nil {
		return backupsource.RestoreReceipt{}, ErrInvalidComposition
	}
	if err := ctx.Err(); err != nil {
		return backupsource.RestoreReceipt{}, err
	}
	// Read once more after the commit point. This is intentionally separate
	// from Validate: it is the receipt probe, not an inference from a method
	// return value or a renamed directory.
	if _, err := backupsource.ValidateRestoredState(transaction.target.root, transaction.fingerprint, transaction.target.limits); err != nil {
		return backupsource.RestoreReceipt{}, fmt.Errorf("reread canonical root after restore commit: %w", err)
	}
	if err := transaction.close(); err != nil {
		return backupsource.RestoreReceipt{}, err
	}
	return backupsource.RestoreReceipt{ReceiptID: "canonical-root:" + transaction.fingerprint, Fingerprint: transaction.fingerprint}, nil
}

func (transaction *canonicalRootTransaction) Rollback(context.Context) error {
	if transaction == nil {
		return ErrInvalidComposition
	}
	return transaction.close()
}

func (transaction *canonicalRootTransaction) close() error {
	if transaction.closed {
		return nil
	}
	transaction.closed = true
	if transaction.release == nil {
		return ErrInvalidComposition
	}
	return transaction.release()
}

func currentState(sources Sources) (*composedState, error) {
	if sources.Settings == nil || sources.Computers == nil || sources.Sessions == nil || sources.Journal == nil {
		return nil, fmt.Errorf("%w: settings, computers, sessions, and journal sources are required", ErrInvalidComposition)
	}
	barrier := NewReadBarrier()
	histories := make(map[domain.Provider]backupsource.HistorySource, 2)
	for _, provider := range []domain.Provider{domain.ProviderCodex, domain.ProviderClaude} {
		root, ok := sources.HistoryRoots[provider]
		if !ok {
			return nil, fmt.Errorf("%w: %s history root is required", ErrInvalidComposition, provider)
		}
		history, err := NewHistorySource(provider, root)
		if err != nil {
			return nil, err
		}
		histories[provider] = history
	}
	harness, err := NewHarnessSource(sources.HarnessRoot)
	if err != nil {
		return nil, err
	}
	state, err := backupsource.New(backupsource.Options{
		Barrier: barrier, Settings: sources.Settings, Computers: sources.Computers, Sessions: sources.Sessions,
		Journal: sources.Journal, Histories: histories, Harness: harness,
	})
	if err != nil {
		return nil, err
	}
	return &composedState{barrier: barrier, state: state}, nil
}

// ReadBarrier is the in-process coordination point that callers must hold
// around live state writes. A snapshot holds a reader lock until its semantic
// documents and histories have been exported; restore callers use BeginWrite.
type ReadBarrier struct{ gate sync.RWMutex }

func NewReadBarrier() *ReadBarrier { return &ReadBarrier{} }

func (barrier *ReadBarrier) BeginRead(ctx context.Context) (func() error, error) {
	if barrier == nil || ctx == nil {
		return nil, ErrInvalidComposition
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	barrier.gate.RLock()
	if err := ctx.Err(); err != nil {
		barrier.gate.RUnlock()
		return nil, err
	}
	return onceRelease(barrier.gate.RUnlock), nil
}

func (barrier *ReadBarrier) BeginWrite(ctx context.Context) (func() error, error) {
	if barrier == nil || ctx == nil {
		return nil, ErrInvalidComposition
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	barrier.gate.Lock()
	if err := ctx.Err(); err != nil {
		barrier.gate.Unlock()
		return nil, err
	}
	return onceRelease(barrier.gate.Unlock), nil
}

func onceRelease(unlock func()) func() error {
	var once sync.Once
	var err error
	return func() error { once.Do(unlock); return err }
}

// HistorySource reads one provider's text-only history from the canonical
// `<root>/<session-id>.jsonl` shape. Symlinks and non-regular files fail.
type HistorySource struct {
	provider domain.Provider
	root     string
}

func NewHistorySource(provider domain.Provider, root string) (*HistorySource, error) {
	if (provider != domain.ProviderCodex && provider != domain.ProviderClaude) || !absolute(root) {
		return nil, fmt.Errorf("%w: provider and absolute history root are required", ErrInvalidComposition)
	}
	return &HistorySource{provider: provider, root: filepath.Clean(root)}, nil
}

func (source *HistorySource) OpenHistory(ctx context.Context, session domain.SessionSnapshot) (io.ReadCloser, error) {
	if source == nil || ctx == nil || session.Provider != source.provider || !safeID(string(session.ID)) {
		return nil, ErrInvalidComposition
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filename := filepath.Join(source.root, string(session.ID)+".jsonl")
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: history file is unavailable or unsafe", ErrInvalidComposition)
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: history file changed while opening", ErrInvalidComposition)
	}
	return file, nil
}

// HarnessSource copies only the four canonical text-only Harness sections.
type HarnessSource struct{ root string }

func NewHarnessSource(root string) (*HarnessSource, error) {
	if !absolute(root) {
		return nil, fmt.Errorf("%w: absolute Harness root is required", ErrInvalidComposition)
	}
	return &HarnessSource{root: filepath.Clean(root)}, nil
}

func (source *HarnessSource) Export(ctx context.Context, sink backupruntime.TreeSink) error {
	if source == nil || sink == nil || ctx == nil {
		return ErrInvalidComposition
	}
	for _, section := range []string{"rules", "settings", "checks", "state"} {
		root := filepath.Join(source.root, section)
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: Harness %s is unavailable or unsafe", ErrInvalidComposition, section)
		}
		var names []string
		err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			if rel != "." && excludedHarnessPath(rel) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				if entry.Type()&os.ModeSymlink != 0 {
					return ErrInvalidComposition
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return ErrInvalidComposition
			}
			if rel == "." || !safeRelative(rel) {
				return ErrInvalidComposition
			}
			names = append(names, rel)
			return nil
		})
		if err != nil {
			return fmt.Errorf("export Harness %s: %w", section, err)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			full := filepath.Join(root, name)
			info, err := os.Lstat(full)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return ErrInvalidComposition
			}
			input, err := os.Open(full)
			if err != nil {
				return err
			}
			opened, statErr := input.Stat()
			if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
				_ = input.Close()
				return ErrInvalidComposition
			}
			writeErr := sink.WriteFile(filepath.ToSlash(filepath.Join(section, name)), input)
			closeErr := input.Close()
			if err := errors.Join(writeErr, closeErr); err != nil {
				return err
			}
		}
	}
	return nil
}

func absolute(value string) bool { return strings.TrimSpace(value) != "" && filepath.IsAbs(value) }
func safeID(value string) bool {
	return value != "" && filepath.Base(value) == value && !strings.ContainsAny(value, "/\\\x00")
}
func safeRelative(value string) bool {
	return value != "" && value != "." && !filepath.IsAbs(value) && !strings.HasPrefix(value, ".."+string(filepath.Separator)) && filepath.Clean(value) == value
}

var excludedHarnessNames = map[string]struct{}{
	"secret": {}, "secrets": {}, "credential": {}, "credentials": {}, "auth": {}, "authorization": {}, "token": {}, "tokens": {}, "key": {}, "keys": {},
	"log": {}, "logs": {}, "media": {}, "photo": {}, "photos": {}, "image": {}, "images": {}, "video": {}, "videos": {}, "voice": {}, "audio": {},
	"file": {}, "files": {}, "output": {}, "outputs": {}, "artifact": {}, "artifacts": {}, "attachment": {}, "attachments": {}, "archive": {}, "archives": {},
	"project": {}, "projects": {}, "workspace": {}, "workspaces": {}, "bin": {}, "binary": {}, "binaries": {}, "executable": {}, "executables": {},
	"parakeet": {}, "model": {}, "models": {}, "cache": {}, "caches": {}, "tmp": {}, "temp": {}, "temporary": {},
}

func excludedHarnessPath(relative string) bool {
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		lower := strings.ToLower(part)
		base := strings.TrimSuffix(lower, filepath.Ext(lower))
		if _, excluded := excludedHarnessNames[base]; excluded {
			return true
		}
	}
	return false
}
