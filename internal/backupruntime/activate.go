package backupruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"bria/internal/backupflow"
)

const activationMarkerVersion = 1

const (
	stagePrepared = "prepared"
	stageOldMoved = "old_moved"
	stageSwapped  = "swapped"
)

type Reopener interface {
	Reopen(context.Context, string, string) (string, error)
}

type ActivatorOptions struct {
	LiveDirectory string
	MarkerPath    string
	Reopener      Reopener
	AfterStage    func(string) error
}

type DirectoryActivator struct {
	liveDir    string
	markerPath string
	previous   string
	reopener   Reopener
	afterStage func(string) error
	mu         sync.Mutex
}

func NewDirectoryActivator(options ActivatorOptions) (*DirectoryActivator, error) {
	if !safeAbsolutePath(options.LiveDirectory) || !safeAbsolutePath(options.MarkerPath) || options.Reopener == nil {
		return nil, fmt.Errorf("%w: live directory, marker path, and reopener are required", ErrInvalidRuntime)
	}
	live := filepath.Clean(options.LiveDirectory)
	marker := filepath.Clean(options.MarkerPath)
	previous := live + ".previous"
	if pathsOverlap(live, marker) || pathsOverlap(previous, marker) {
		return nil, fmt.Errorf("%w: activation paths overlap", ErrInvalidRuntime)
	}
	return &DirectoryActivator{liveDir: live, markerPath: marker, previous: previous, reopener: options.Reopener, afterStage: options.AfterStage}, nil
}

func (activator *DirectoryActivator) Activate(ctx context.Context, request backupflow.ActivationRequest) (backupflow.ActivationReceipt, error) {
	if activator == nil || ctx == nil {
		return backupflow.ActivationReceipt{}, fmt.Errorf("%w: activator and context are required", ErrInvalidRuntime)
	}
	activator.mu.Lock()
	defer activator.mu.Unlock()
	if _, err := os.Lstat(activator.markerPath); err == nil {
		return backupflow.ActivationReceipt{}, errors.New("restore activation recovery is required before a new commit")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return backupflow.ActivationReceipt{}, fmt.Errorf("inspect restore marker: %w", err)
	}
	marker, err := activator.newMarker(request)
	if err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	if err := writeActivationMarker(activator.markerPath, marker); err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	if err := activator.after(stagePrepared); err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	if exists, err := secureDirectoryExists(activator.liveDir); err != nil {
		return backupflow.ActivationReceipt{}, err
	} else if exists {
		if err := os.Rename(activator.liveDir, activator.previous); err != nil {
			return backupflow.ActivationReceipt{}, fmt.Errorf("move previous live state: %w", err)
		}
		if err := syncDirectory(filepath.Dir(activator.liveDir)); err != nil {
			return backupflow.ActivationReceipt{}, err
		}
	}
	marker.Stage = stageOldMoved
	if err := writeActivationMarker(activator.markerPath, marker); err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	if err := activator.after(stageOldMoved); err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	if err := os.Rename(marker.CandidateDir, activator.liveDir); err != nil {
		return backupflow.ActivationReceipt{}, fmt.Errorf("commit restored state: %w", err)
	}
	if err := syncDirectory(filepath.Dir(activator.liveDir)); err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	marker.Stage = stageSwapped
	if err := writeActivationMarker(activator.markerPath, marker); err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	if err := activator.after(stageSwapped); err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	return activator.finish(ctx, marker)
}

func (activator *DirectoryActivator) Recover(ctx context.Context) (backupflow.ActivationReceipt, bool, error) {
	if activator == nil || ctx == nil {
		return backupflow.ActivationReceipt{}, false, fmt.Errorf("%w: activator and context are required", ErrInvalidRuntime)
	}
	activator.mu.Lock()
	defer activator.mu.Unlock()
	marker, exists, err := readActivationMarker(activator.markerPath)
	if err != nil || !exists {
		return backupflow.ActivationReceipt{}, false, err
	}
	if err := activator.validateMarker(marker); err != nil {
		return backupflow.ActivationReceipt{}, true, err
	}
	liveExists, err := secureDirectoryExists(activator.liveDir)
	if err != nil {
		return backupflow.ActivationReceipt{}, true, err
	}
	candidateExists, err := secureDirectoryExists(marker.CandidateDir)
	if err != nil {
		return backupflow.ActivationReceipt{}, true, err
	}
	previousExists, err := secureDirectoryExists(activator.previous)
	if err != nil {
		return backupflow.ActivationReceipt{}, true, err
	}

	if candidateExists && !liveExists && (marker.Stage == stageOldMoved || previousExists) {
		if err := os.Rename(marker.CandidateDir, activator.liveDir); err != nil {
			return backupflow.ActivationReceipt{}, true, fmt.Errorf("resume restore commit: %w", err)
		}
		if err := syncDirectory(filepath.Dir(activator.liveDir)); err != nil {
			return backupflow.ActivationReceipt{}, true, err
		}
		marker.Stage = stageSwapped
		if err := writeActivationMarker(activator.markerPath, marker); err != nil {
			return backupflow.ActivationReceipt{}, true, err
		}
		returnReceipt, err := activator.finish(ctx, marker)
		return returnReceipt, true, err
	}
	if liveExists && !candidateExists && (marker.Stage == stageSwapped || marker.Stage == stageOldMoved || previousExists) {
		returnReceipt, err := activator.finish(ctx, marker)
		return returnReceipt, true, err
	}
	if candidateExists && liveExists && !previousExists && marker.Stage == stagePrepared {
		if err := removeMarker(activator.markerPath); err != nil {
			return backupflow.ActivationReceipt{}, true, err
		}
		return backupflow.ActivationReceipt{}, true, nil
	}
	if candidateExists && !liveExists && !previousExists && marker.Stage == stagePrepared {
		if err := removeMarker(activator.markerPath); err != nil {
			return backupflow.ActivationReceipt{}, true, err
		}
		return backupflow.ActivationReceipt{}, true, nil
	}
	return backupflow.ActivationReceipt{}, true, errors.New("restore activation marker does not match filesystem state")
}

type activationMarker struct {
	Version      int    `json:"version"`
	Stage        string `json:"stage"`
	CandidateDir string `json:"candidate_dir"`
	LiveDir      string `json:"live_dir"`
	PreviousDir  string `json:"previous_dir"`
	ComputerID   string `json:"computer_id"`
	Fingerprint  string `json:"fingerprint"`
}

func (activator *DirectoryActivator) newMarker(request backupflow.ActivationRequest) (activationMarker, error) {
	if !safeAbsolutePath(request.CandidateDir) || strings.TrimSpace(request.ComputerID) == "" || strings.TrimSpace(request.Fingerprint) == "" {
		return activationMarker{}, fmt.Errorf("%w: activation request is incomplete", ErrInvalidRuntime)
	}
	candidate := filepath.Clean(request.CandidateDir)
	if pathsOverlap(candidate, activator.liveDir) || pathsOverlap(candidate, activator.previous) || pathsOverlap(candidate, activator.markerPath) {
		return activationMarker{}, fmt.Errorf("%w: candidate overlaps activation state", ErrInvalidRuntime)
	}
	if exists, err := secureDirectoryExists(candidate); err != nil {
		return activationMarker{}, err
	} else if !exists {
		return activationMarker{}, errors.New("restore candidate directory is unavailable")
	}
	if exists, err := pathExists(activator.previous); err != nil {
		return activationMarker{}, err
	} else if exists {
		return activationMarker{}, errors.New("previous restore state requires recovery")
	}
	return activationMarker{Version: activationMarkerVersion, Stage: stagePrepared, CandidateDir: candidate, LiveDir: activator.liveDir, PreviousDir: activator.previous, ComputerID: request.ComputerID, Fingerprint: request.Fingerprint}, nil
}

func (activator *DirectoryActivator) finish(ctx context.Context, marker activationMarker) (backupflow.ActivationReceipt, error) {
	receiptID, err := activator.reopener.Reopen(ctx, activator.liveDir, marker.Fingerprint)
	if err != nil || strings.TrimSpace(receiptID) == "" {
		if err == nil {
			err = errors.New("reopen returned no receipt")
		}
		return backupflow.ActivationReceipt{}, errors.Join(err, activator.rollback(marker))
	}
	if exists, inspectErr := pathExists(activator.previous); inspectErr != nil {
		return backupflow.ActivationReceipt{}, inspectErr
	} else if exists {
		if removeErr := os.RemoveAll(activator.previous); removeErr != nil {
			return backupflow.ActivationReceipt{}, fmt.Errorf("remove previous state after reopen: %w", removeErr)
		}
	}
	if err := removeMarker(activator.markerPath); err != nil {
		return backupflow.ActivationReceipt{}, err
	}
	return backupflow.ActivationReceipt{ReceiptID: receiptID, CandidateDir: marker.CandidateDir, ComputerID: marker.ComputerID, Fingerprint: marker.Fingerprint}, nil
}

func (activator *DirectoryActivator) rollback(marker activationMarker) error {
	liveExists, liveErr := secureDirectoryExists(activator.liveDir)
	previousExists, previousErr := secureDirectoryExists(activator.previous)
	if liveErr != nil || previousErr != nil {
		return errors.Join(liveErr, previousErr)
	}
	if liveExists {
		if exists, err := pathExists(marker.CandidateDir); err != nil {
			return err
		} else if exists {
			return errors.New("cannot preserve failed restored state because candidate path exists")
		}
		if err := os.Rename(activator.liveDir, marker.CandidateDir); err != nil {
			return fmt.Errorf("preserve failed restored state: %w", err)
		}
	}
	if previousExists {
		if err := os.Rename(activator.previous, activator.liveDir); err != nil {
			return fmt.Errorf("roll back previous live state: %w", err)
		}
	}
	if err := syncDirectory(filepath.Dir(activator.liveDir)); err != nil {
		return err
	}
	return removeMarker(activator.markerPath)
}

func (activator *DirectoryActivator) validateMarker(marker activationMarker) error {
	if marker.Version != activationMarkerVersion || (marker.Stage != stagePrepared && marker.Stage != stageOldMoved && marker.Stage != stageSwapped) || marker.LiveDir != activator.liveDir || marker.PreviousDir != activator.previous || !safeAbsolutePath(marker.CandidateDir) || strings.TrimSpace(marker.ComputerID) == "" || strings.TrimSpace(marker.Fingerprint) == "" {
		return errors.New("invalid restore activation marker")
	}
	if pathsOverlap(marker.CandidateDir, activator.liveDir) || pathsOverlap(marker.CandidateDir, activator.previous) || pathsOverlap(marker.CandidateDir, activator.markerPath) {
		return errors.New("invalid restore activation marker paths")
	}
	return nil
}

func writeActivationMarker(markerPath string, marker activationMarker) error {
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		return fmt.Errorf("create restore marker directory: %w", err)
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(markerPath), ".restore-marker-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	_, writeErr := temporary.Write(encoded)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, markerPath); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(markerPath))
}

func readActivationMarker(markerPath string) (activationMarker, bool, error) {
	info, err := os.Lstat(markerPath)
	if errors.Is(err, fs.ErrNotExist) {
		return activationMarker{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
		return activationMarker{}, true, errors.New("restore activation marker is unsafe")
	}
	input, err := os.Open(markerPath)
	if err != nil {
		return activationMarker{}, true, err
	}
	defer input.Close()
	decoder := json.NewDecoder(io.LimitReader(input, 64<<10))
	decoder.DisallowUnknownFields()
	var marker activationMarker
	if err := decoder.Decode(&marker); err != nil {
		return activationMarker{}, true, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return activationMarker{}, true, errors.New("restore activation marker has trailing data")
	}
	return marker, true, nil
}

func removeMarker(markerPath string) error {
	if err := os.Remove(markerPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(markerPath))
}

func secureDirectoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("restore state path is not a regular directory")
	}
	return true, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func safeAbsolutePath(value string) bool {
	return strings.TrimSpace(value) != "" && filepath.IsAbs(value) && filepath.Clean(value) != string(filepath.Separator)
}

func pathsOverlap(left, right string) bool {
	return filepathContains(left, right) || filepathContains(right, left)
}

func filepathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func (activator *DirectoryActivator) after(stage string) error {
	if activator.afterStage == nil {
		return nil
	}
	return activator.afterStage(stage)
}
