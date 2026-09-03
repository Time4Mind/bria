package backupsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bria/internal/computer"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/settings"
)

type RestoreLimits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

type HistoryEntry struct {
	SessionID domain.SessionID
	Provider  domain.Provider
	Path      string
}

type RestoredState struct {
	Fingerprint string
	Settings    settings.Snapshot
	Computers   computer.CatalogSnapshot
	Sessions    []domain.SessionSnapshot
	Undelivered UndeliveredState
	Histories   []HistoryEntry
	HarnessRoot string
}

// RestoreTarget owns the atomic multi-store import. Implementations must keep
// their previous state until Reopen succeeds and make Rollback idempotent. It
// must rebuild each UndeliveredSession by enqueuing its Records in order into
// a fresh journal; SourceSequence is never assigned as a durable sequence.
type RestoreTarget interface {
	PrepareRestore(context.Context, RestoredState) (RestoreTransaction, error)
}

type RestoreTransaction interface {
	Validate(context.Context) error
	Commit(context.Context) error
	Reopen(context.Context) (RestoreReceipt, error)
	Rollback(context.Context) error
}

type RestoreReceipt struct {
	ReceiptID   string
	Fingerprint string
}

type ReopenerOptions struct {
	Target RestoreTarget
	Limits RestoreLimits
}

type Reopener struct{ options ReopenerOptions }

func NewReopener(options ReopenerOptions) (*Reopener, error) {
	if options.Target == nil || options.Limits.MaxFiles < 8 || options.Limits.MaxFileBytes <= 0 || options.Limits.MaxTotalBytes < options.Limits.MaxFileBytes {
		return nil, fmt.Errorf("%w: restore target and positive limits are required", ErrInvalidSource)
	}
	return &Reopener{options: options}, nil
}

func (reopener *Reopener) Reopen(ctx context.Context, liveRoot, fingerprint string) (string, error) {
	if reopener == nil || ctx == nil || strings.TrimSpace(fingerprint) == "" {
		return "", ErrInvalidSource
	}
	restored, err := readRestoredState(liveRoot, fingerprint, reopener.options.Limits)
	if err != nil {
		return "", err
	}
	transaction, err := reopener.options.Target.PrepareRestore(ctx, restored)
	if err != nil {
		return "", fmt.Errorf("prepare typed restore: %w", err)
	}
	if transaction == nil {
		return "", fmt.Errorf("%w: restore target returned no transaction", ErrInvalidSource)
	}
	rollback := func(failure error) (string, error) {
		return "", errors.Join(failure, transaction.Rollback(context.WithoutCancel(ctx)))
	}
	if err := transaction.Validate(ctx); err != nil {
		return rollback(fmt.Errorf("validate typed restore: %w", err))
	}
	if err := transaction.Commit(ctx); err != nil {
		return rollback(fmt.Errorf("commit typed restore: %w", err))
	}
	receipt, err := transaction.Reopen(ctx)
	if err != nil || strings.TrimSpace(receipt.ReceiptID) == "" || receipt.Fingerprint != fingerprint {
		if err == nil {
			err = fmt.Errorf("%w: typed restore receipt does not match restored fingerprint", ErrInvalidSource)
		}
		return rollback(err)
	}
	return receipt.ReceiptID, nil
}

// ValidateRestoredState rereads every canonical restore member from root.
// It is the public physical-state verification seam used by an atomic root
// activator after it has switched a prepared directory into place.
func ValidateRestoredState(root, fingerprint string, limits RestoreLimits) (RestoredState, error) {
	if strings.TrimSpace(fingerprint) == "" || limits.MaxFiles < 8 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes < limits.MaxFileBytes {
		return RestoredState{}, ErrInvalidSource
	}
	return readRestoredState(root, fingerprint, limits)
}

type settingsDocument struct {
	Version  int               `json:"version"`
	Revision uint64            `json:"revision"`
	Settings settings.Settings `json:"settings"`
}
type computerDocument struct {
	Version   int                      `json:"version"`
	Computers computer.CatalogSnapshot `json:"catalog"`
}
type sessionDocument struct {
	Version  int                      `json:"version"`
	Sessions []domain.SessionSnapshot `json:"sessions"`
}

func readRestoredState(root, fingerprint string, limits RestoreLimits) (RestoredState, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return RestoredState{}, fmt.Errorf("%w: restored semantic root is unsafe", ErrInvalidSource)
	}
	budget := &restoreBudget{limits: limits}
	var settingsDoc settingsDocument
	if err := decodeDocument(filepath.Join(root, "settings", "bria.json"), budget, &settingsDoc); err != nil {
		return RestoredState{}, err
	}
	if settingsDoc.Version != 1 || settingsDoc.Settings.Validate() != nil {
		return RestoredState{}, fmt.Errorf("%w: invalid restored settings", ErrInvalidSource)
	}
	var computersDoc computerDocument
	if err := decodeDocument(filepath.Join(root, "computers", "catalog.json"), budget, &computersDoc); err != nil {
		return RestoredState{}, err
	}
	if computersDoc.Version != 1 {
		return RestoredState{}, fmt.Errorf("%w: invalid computer document", ErrInvalidSource)
	}
	if _, err := computer.RestoreCatalog(computersDoc.Computers); err != nil {
		return RestoredState{}, err
	}
	var sessionsDoc sessionDocument
	if err := decodeDocument(filepath.Join(root, "sessions", "state.json"), budget, &sessionsDoc); err != nil {
		return RestoredState{}, err
	}
	if sessionsDoc.Version != 1 {
		return RestoredState{}, fmt.Errorf("%w: invalid session document", ErrInvalidSource)
	}
	seenSessions := make(map[domain.SessionID]domain.SessionSnapshot, len(sessionsDoc.Sessions))
	for _, snapshot := range sessionsDoc.Sessions {
		if _, duplicate := seenSessions[snapshot.ID]; duplicate {
			return RestoredState{}, fmt.Errorf("%w: duplicate restored session", ErrInvalidSource)
		}
		if _, err := domain.RestoreSession(snapshot); err != nil {
			return RestoredState{}, err
		}
		seenSessions[snapshot.ID] = snapshot
	}
	var undelivered UndeliveredState
	if err := decodeDocument(filepath.Join(root, "messages", "undelivered.json"), budget, &undelivered); err != nil {
		return RestoredState{}, err
	}
	if err := validateUndelivered(undelivered, seenSessions); err != nil {
		return RestoredState{}, err
	}
	histories, err := readHistoryEntries(filepath.Join(root, "history", "text"), seenSessions, budget)
	if err != nil {
		return RestoredState{}, err
	}
	harnessRoot := filepath.Join(root, "harness")
	if err := validateHarness(harnessRoot, budget); err != nil {
		return RestoredState{}, err
	}
	return RestoredState{Fingerprint: fingerprint, Settings: settings.Snapshot{Revision: settingsDoc.Revision, Settings: settingsDoc.Settings}, Computers: computersDoc.Computers, Sessions: sessionsDoc.Sessions, Undelivered: undelivered, Histories: histories, HarnessRoot: harnessRoot}, nil
}

type restoreBudget struct {
	limits RestoreLimits
	files  int
	total  int64
}

func decodeDocument(path string, budget *restoreBudget, target any) error {
	data, err := readBoundedFile(path, budget)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode restored document: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("restored document has trailing data")
	}
	return nil
}
func readBoundedFile(path string, budget *restoreBudget) ([]byte, error) {
	if budget.files >= budget.limits.MaxFiles {
		return nil, fmt.Errorf("%w: restored state exceeds limits", ErrInvalidSource)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: restored file is unsafe", ErrInvalidSource)
	}
	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: restored file is unavailable", ErrInvalidSource)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) || info.Size() > budget.limits.MaxFileBytes {
		return nil, fmt.Errorf("%w: restored file is unsafe or oversized", ErrInvalidSource)
	}
	remainingTotal := budget.limits.MaxTotalBytes - budget.total
	if remainingTotal < 0 || info.Size() > remainingTotal {
		return nil, fmt.Errorf("%w: restored state exceeds limits", ErrInvalidSource)
	}
	readLimit := budget.limits.MaxFileBytes
	if remainingTotal < readLimit {
		readLimit = remainingTotal
	}
	data, err := io.ReadAll(io.LimitReader(input, readLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > readLimit || int64(len(data)) != info.Size() {
		return nil, fmt.Errorf("%w: restored file changed or exceeds limits", ErrInvalidSource)
	}
	budget.files++
	budget.total += int64(len(data))
	return data, nil
}
func readHistoryEntries(root string, sessions map[domain.SessionID]domain.SessionSnapshot, budget *restoreBudget) ([]HistoryEntry, error) {
	want := make(map[string]HistoryEntry, len(sessions))
	for _, s := range sessions {
		name, err := historyName(s)
		if err != nil {
			return nil, err
		}
		want[filepath.FromSlash(name)] = HistoryEntry{SessionID: s.ID, Provider: s.Provider, Path: filepath.Join(root, filepath.FromSlash(name))}
	}
	got := make(map[string]struct{})
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		item, ok := want[rel]
		if !ok {
			return fmt.Errorf("%w: unexpected history file", ErrInvalidSource)
		}
		if _, err := readBoundedFile(item.Path, budget); err != nil {
			return err
		}
		got[rel] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(got) != len(want) {
		return nil, errors.New("restored history set is incomplete")
	}
	result := make([]HistoryEntry, 0, len(want))
	for _, item := range want {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SessionID < result[j].SessionID })
	return result, nil
}
func validateHarness(root string, budget *restoreBudget) error {
	sections := map[string]bool{"rules": false, "settings": false, "checks": false, "state": false}
	for section := range sections {
		info, err := os.Lstat(filepath.Join(root, section))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: Harness section missing", ErrInvalidSource)
		}
	}
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		_, err := readBoundedFile(current, budget)
		return err
	})
}
func validateUndelivered(state UndeliveredState, sessions map[domain.SessionID]domain.SessionSnapshot) error {
	if state.Version != 1 {
		return fmt.Errorf("%w: invalid undelivered version", ErrInvalidSource)
	}
	seen := map[domain.SessionID]struct{}{}
	for _, s := range state.Sessions {
		if _, ok := sessions[s.SessionID]; !ok {
			return fmt.Errorf("%w: undelivered session unknown", ErrInvalidSource)
		}
		if _, dupe := seen[s.SessionID]; dupe {
			return fmt.Errorf("%w: duplicate undelivered session", ErrInvalidSource)
		}
		seen[s.SessionID] = struct{}{}
		var previousSourceSequence uint64
		inputIDs := make(map[string]struct{})
		outputIDs := make(map[string]struct{})
		for _, record := range s.Records {
			if record.SourceSequence <= previousSourceSequence || (record.Input == nil) == (record.Output == nil) {
				return fmt.Errorf("%w: invalid undelivered queue order", ErrInvalidSource)
			}
			previousSourceSequence = record.SourceSequence
			if record.Input != nil {
				input := record.Input
				if strings.TrimSpace(input.MessageID) != input.MessageID || input.MessageID == "" || (input.Phase != messagejournal.InputPending && input.Phase != messagejournal.InputFailed && input.Phase != messagejournal.InputUnknown) {
					return fmt.Errorf("%w: invalid undelivered input", ErrInvalidSource)
				}
				if _, duplicate := inputIDs[input.MessageID]; duplicate {
					return fmt.Errorf("%w: duplicate undelivered input identity", ErrInvalidSource)
				}
				inputIDs[input.MessageID] = struct{}{}
				continue
			}
			output := record.Output
			if strings.TrimSpace(output.OperationID) != output.OperationID || output.OperationID == "" || strings.TrimSpace(output.Kind) != output.Kind || output.Kind == "" || (output.Phase != messagejournal.OutputPending && output.Phase != messagejournal.OutputFailed && output.Phase != messagejournal.OutputUnknown) {
				return fmt.Errorf("%w: invalid undelivered output", ErrInvalidSource)
			}
			if _, duplicate := outputIDs[output.OperationID]; duplicate {
				return fmt.Errorf("%w: duplicate undelivered output identity", ErrInvalidSource)
			}
			outputIDs[output.OperationID] = struct{}{}
		}
	}
	return nil
}
