// Package backupruntime composes semantic state exports with local backup and restore.
package backupruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bria/internal/backup"
	"bria/internal/backupflow"
)

var (
	ErrInvalidRuntime = errors.New("invalid backup runtime")
	ErrManualOnly     = errors.New("backup scheduler is manual-only")
)

type DocumentSource interface {
	Export(context.Context, io.Writer) error
}

type TreeSink interface{ WriteFile(string, io.Reader) error }

type TreeSource interface {
	Export(context.Context, TreeSink) error
}

type Sources struct {
	Settings            DocumentSource
	Computers           DocumentSource
	Sessions            DocumentSource
	UndeliveredMessages DocumentSource
	TextHistory         TreeSource
	Harness             TreeSource
}

type SnapshotTransaction interface {
	Sources() Sources
	Close() error
}

type CurrentState interface {
	BeginSnapshot(context.Context) (SnapshotTransaction, error)
}

type RunOptions struct {
	WorkDirectory      string
	LatestPath         string
	ComputerID         string
	State              CurrentState
	RemoteRequired     bool
	Remote             RemoteLatestStore
	EncryptionRequired bool
	Protector          ArtifactProtector
	Limits             Limits
}

type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

type ExternalReceipt struct{ ReceiptID, Fingerprint string }

type ProtectedCopy struct{ Path, SourceFingerprint, Fingerprint, ReceiptID string }

type ArtifactProtector interface {
	Protect(context.Context, backupflow.VerifiedCopy, string) (ProtectedCopy, error)
}

type RemoteLatestStore interface {
	ReplaceLatest(context.Context, string, string) (ExternalReceipt, error)
}

type Result struct {
	LatestPath          string
	Manifest            backup.Manifest
	BackupFingerprint   string
	ArtifactFingerprint string
	Encrypted           bool
	ProtectionReceiptID string
	Remote              *ExternalReceipt
}

type Runner struct{ options RunOptions }

func NewRunner(options RunOptions) (*Runner, error) {
	if options.State == nil || strings.TrimSpace(options.WorkDirectory) == "" || strings.TrimSpace(options.LatestPath) == "" || strings.TrimSpace(options.ComputerID) == "" {
		return nil, fmt.Errorf("%w: state, work directory, latest path, and computer id are required", ErrInvalidRuntime)
	}
	if !filepath.IsAbs(options.WorkDirectory) || !filepath.IsAbs(options.LatestPath) {
		return nil, fmt.Errorf("%w: work and latest paths must be absolute", ErrInvalidRuntime)
	}
	if err := options.Limits.validate(); err != nil {
		return nil, err
	}
	if options.RemoteRequired != (options.Remote != nil) {
		return nil, fmt.Errorf("%w: remote policy and port must be configured together", ErrInvalidRuntime)
	}
	if options.EncryptionRequired != (options.Protector != nil) {
		return nil, fmt.Errorf("%w: encryption policy and protector must be configured together", ErrInvalidRuntime)
	}
	return &Runner{options: options}, nil
}

func (runner *Runner) RunOnce(ctx context.Context) (result Result, returnErr error) {
	if runner == nil {
		return Result{}, fmt.Errorf("%w: runner is required", ErrInvalidRuntime)
	}
	if ctx == nil {
		return Result{}, fmt.Errorf("%w: context is required", ErrInvalidRuntime)
	}
	if err := os.MkdirAll(runner.options.WorkDirectory, 0o700); err != nil {
		return Result{}, fmt.Errorf("create backup work directory: %w", err)
	}
	workInfo, err := os.Lstat(runner.options.WorkDirectory)
	if err != nil || !workInfo.IsDir() || workInfo.Mode()&os.ModeSymlink != 0 {
		return Result{}, fmt.Errorf("%w: work directory is unsafe", ErrInvalidRuntime)
	}
	staging, err := os.MkdirTemp(runner.options.WorkDirectory, ".snapshot-")
	if err != nil {
		return Result{}, fmt.Errorf("create semantic snapshot staging: %w", err)
	}
	defer os.RemoveAll(staging)

	transaction, err := runner.options.State.BeginSnapshot(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin semantic snapshot: %w", err)
	}
	if transaction == nil {
		return Result{}, fmt.Errorf("%w: current state returned no transaction", ErrInvalidRuntime)
	}
	transactionClosed := false
	defer func() {
		if !transactionClosed {
			closeErr := transaction.Close()
			transactionClosed = true
			if closeErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close semantic snapshot: %w", closeErr))
			}
		}
	}()
	sources := transaction.Sources()
	if err := validateSources(sources); err != nil {
		return Result{}, err
	}
	if err := exportSnapshot(ctx, staging, sources, runner.options.Limits); err != nil {
		return Result{}, err
	}
	closeErr := transaction.Close()
	transactionClosed = true
	if closeErr != nil {
		return Result{}, fmt.Errorf("close semantic snapshot before backup: %w", closeErr)
	}

	backupLatest := runner.options.LatestPath
	if runner.options.EncryptionRequired {
		backupLatest = filepath.Join(staging, ".verified-plaintext.bria-backup")
	}
	flow := backupflow.Service{
		SourceRoot: staging, LatestPath: backupLatest,
		RestoreCandidateDir: filepath.Join(runner.options.WorkDirectory, ".restore-unused"),
		ComputerID:          runner.options.ComputerID, Layout: backupflow.CanonicalSnapshotLayout(),
	}
	local, err := flow.CreateLatest()
	if err != nil {
		return Result{}, fmt.Errorf("create verified local backup: %w", err)
	}
	result = Result{
		LatestPath: runner.options.LatestPath, Manifest: local.Manifest,
		BackupFingerprint: local.Fingerprint, ArtifactFingerprint: local.Fingerprint,
	}
	artifactPath, artifactFingerprint := local.Path, local.Fingerprint
	if runner.options.EncryptionRequired {
		protectedCandidate := filepath.Join(staging, ".protected-backup.candidate")
		protected, err := runner.options.Protector.Protect(ctx, local, protectedCandidate)
		if err != nil {
			return Result{}, fmt.Errorf("protect backup artifact: %w", err)
		}
		if protected.Path != protectedCandidate || protected.SourceFingerprint != local.Fingerprint || strings.TrimSpace(protected.Fingerprint) == "" || strings.TrimSpace(protected.ReceiptID) == "" {
			return Result{}, fmt.Errorf("%w: protector returned no exact receipt", ErrInvalidRuntime)
		}
		protectedFingerprint, err := validateOpaqueArtifact(protected.Path, runner.options.Limits.MaxTotalBytes)
		if err != nil {
			return Result{}, err
		}
		if protected.Fingerprint != protectedFingerprint {
			return Result{}, fmt.Errorf("%w: protected artifact checksum does not match receipt", ErrInvalidRuntime)
		}
		if err := promoteOpaqueCandidate(protected.Path, runner.options.LatestPath); err != nil {
			return Result{}, err
		}
		result.Encrypted = true
		result.ProtectionReceiptID = protected.ReceiptID
		result.ArtifactFingerprint = protected.Fingerprint
		artifactPath, artifactFingerprint = runner.options.LatestPath, protected.Fingerprint
	}
	if runner.options.RemoteRequired {
		receipt, err := runner.options.Remote.ReplaceLatest(ctx, artifactPath, artifactFingerprint)
		if err != nil {
			return Result{}, fmt.Errorf("replace remote latest backup: %w", err)
		}
		if strings.TrimSpace(receipt.ReceiptID) == "" || receipt.Fingerprint != artifactFingerprint {
			return Result{}, fmt.Errorf("%w: remote store returned no exact receipt", ErrInvalidRuntime)
		}
		result.Remote = &receipt
	}
	return result, nil
}

type OnceRunner interface {
	RunOnce(context.Context) (Result, error)
}

type Scheduler struct {
	runner   OnceRunner
	interval time.Duration
	mu       sync.Mutex
}

func NewScheduler(runner OnceRunner, interval time.Duration) (*Scheduler, error) {
	if runner == nil || interval < 0 {
		return nil, fmt.Errorf("%w: runner is required and interval cannot be negative", ErrInvalidRuntime)
	}
	return &Scheduler{runner: runner, interval: interval}, nil
}

func (scheduler *Scheduler) RunOnce(ctx context.Context) (Result, error) {
	if scheduler == nil || scheduler.runner == nil {
		return Result{}, fmt.Errorf("%w: scheduler is required", ErrInvalidRuntime)
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.runner.RunOnce(ctx)
}

func (scheduler *Scheduler) Run(ctx context.Context) error {
	if scheduler == nil || scheduler.runner == nil || ctx == nil {
		return fmt.Errorf("%w: scheduler and context are required", ErrInvalidRuntime)
	}
	if scheduler.interval == 0 {
		return ErrManualOnly
	}
	ticker := time.NewTicker(scheduler.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := scheduler.RunOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func validateSources(sources Sources) error {
	if sources.Settings == nil || sources.Computers == nil || sources.Sessions == nil || sources.UndeliveredMessages == nil || sources.TextHistory == nil || sources.Harness == nil {
		return fmt.Errorf("%w: all six semantic snapshot sources are required", ErrInvalidRuntime)
	}
	return nil
}

func exportSnapshot(ctx context.Context, root string, sources Sources, limits Limits) error {
	budget := &snapshotBudget{limits: limits}
	documents := []struct {
		path   string
		source DocumentSource
	}{
		{path: "settings/bria.json", source: sources.Settings},
		{path: "computers/catalog.json", source: sources.Computers},
		{path: "sessions/state.json", source: sources.Sessions},
		{path: "messages/undelivered.json", source: sources.UndeliveredMessages},
	}
	for _, document := range documents {
		if err := exportDocument(ctx, filepath.Join(root, filepath.FromSlash(document.path)), document.source, budget); err != nil {
			return fmt.Errorf("export %s: %w", document.path, err)
		}
	}
	historyRoot := filepath.Join(root, "history", "text")
	if err := os.MkdirAll(historyRoot, 0o700); err != nil {
		return fmt.Errorf("create text history export: %w", err)
	}
	if err := sources.TextHistory.Export(ctx, &treeWriter{root: historyRoot, written: make(map[string]struct{}), budget: budget}); err != nil {
		return fmt.Errorf("export text history: %w", err)
	}
	harnessRoot := filepath.Join(root, "harness")
	for _, section := range []string{"rules", "settings", "checks", "state"} {
		if err := os.MkdirAll(filepath.Join(harnessRoot, section), 0o700); err != nil {
			return fmt.Errorf("create Harness section %s: %w", section, err)
		}
	}
	if err := sources.Harness.Export(ctx, &harnessTreeWriter{treeWriter: treeWriter{root: harnessRoot, written: make(map[string]struct{}), budget: budget}}); err != nil {
		return fmt.Errorf("export Harness: %w", err)
	}
	return nil
}

func exportDocument(ctx context.Context, target string, source DocumentSource, budget *snapshotBudget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	limited, err := budget.startFile()
	if err != nil {
		_ = output.Close()
		return err
	}
	exportErr := source.Export(ctx, io.MultiWriter(limited, output))
	if limited.exceeded {
		exportErr = errors.Join(exportErr, fmt.Errorf("%w: semantic snapshot document exceeds limits", ErrInvalidRuntime))
	}
	syncErr := output.Sync()
	closeErr := output.Close()
	return errors.Join(exportErr, syncErr, closeErr)
}

type treeWriter struct {
	root    string
	written map[string]struct{}
	budget  *snapshotBudget
}

func (writer *treeWriter) WriteFile(relative string, content io.Reader) error {
	if writer == nil || content == nil {
		return fmt.Errorf("%w: tree writer and content are required", ErrInvalidRuntime)
	}
	if relative == "" || strings.Contains(relative, "\\") || path.IsAbs(relative) || path.Clean(relative) != relative || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return fmt.Errorf("%w: tree path must be canonical and relative", ErrInvalidRuntime)
	}
	if _, duplicate := writer.written[relative]; duplicate {
		return fmt.Errorf("%w: duplicate tree path %q", ErrInvalidRuntime, relative)
	}
	target := filepath.Join(writer.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	limited, limitErr := writer.budget.startFile()
	if limitErr != nil {
		_ = output.Close()
		return limitErr
	}
	_, copyErr := io.Copy(io.MultiWriter(limited, output), content)
	if limited.exceeded {
		copyErr = errors.Join(copyErr, fmt.Errorf("%w: semantic snapshot file exceeds limits", ErrInvalidRuntime))
	}
	syncErr := output.Sync()
	closeErr := output.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	writer.written[relative] = struct{}{}
	return nil
}

type harnessTreeWriter struct{ treeWriter }

func (writer *harnessTreeWriter) WriteFile(relative string, content io.Reader) error {
	clean := path.Clean(relative)
	section := strings.SplitN(clean, "/", 2)
	if len(section) != 2 || (section[0] != "rules" && section[0] != "settings" && section[0] != "checks" && section[0] != "state") {
		return fmt.Errorf("%w: Harness files require rules, settings, checks, or state section", ErrInvalidRuntime)
	}
	return writer.treeWriter.WriteFile(relative, content)
}

type snapshotBudget struct {
	limits Limits
	files  int
	total  int64
}

func (limits Limits) validate() error {
	if limits.MaxFiles < 4 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes < limits.MaxFileBytes {
		return fmt.Errorf("%w: positive snapshot limits are required", ErrInvalidRuntime)
	}
	return nil
}

func (budget *snapshotBudget) startFile() (*budgetWriter, error) {
	if budget.files >= budget.limits.MaxFiles {
		return nil, fmt.Errorf("%w: semantic snapshot file count exceeded", ErrInvalidRuntime)
	}
	budget.files++
	return &budgetWriter{budget: budget}, nil
}

type budgetWriter struct {
	budget   *snapshotBudget
	written  int64
	exceeded bool
}

func (writer *budgetWriter) Write(content []byte) (int, error) {
	remainingFile := writer.budget.limits.MaxFileBytes - writer.written
	remainingTotal := writer.budget.limits.MaxTotalBytes - writer.budget.total
	remaining := remainingFile
	if remainingTotal < remaining {
		remaining = remainingTotal
	}
	if remaining < int64(len(content)) {
		writer.exceeded = true
		if remaining <= 0 {
			return 0, fmt.Errorf("%w: semantic snapshot byte limit exceeded", ErrInvalidRuntime)
		}
		content = content[:remaining]
	}
	written := len(content)
	writer.written += int64(written)
	writer.budget.total += int64(written)
	if writer.exceeded {
		return written, fmt.Errorf("%w: semantic snapshot byte limit exceeded", ErrInvalidRuntime)
	}
	return written, nil
}

func validateOpaqueArtifact(artifactPath string, maxBytes int64) (string, error) {
	info, err := os.Lstat(artifactPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxBytes {
		return "", fmt.Errorf("%w: protected artifact is missing, unsafe, empty, or oversized", ErrInvalidRuntime)
	}
	input, err := os.Open(artifactPath)
	if err != nil {
		return "", err
	}
	defer input.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(input, maxBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func promoteOpaqueCandidate(candidatePath, latestPath string) error {
	if err := os.MkdirAll(filepath.Dir(latestPath), 0o700); err != nil {
		return err
	}
	if err := os.Rename(candidatePath, latestPath); err != nil {
		return fmt.Errorf("promote protected latest backup: %w", err)
	}
	return syncRuntimeDirectory(filepath.Dir(latestPath))
}

func syncRuntimeDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
