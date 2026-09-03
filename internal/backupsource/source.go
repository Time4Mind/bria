// Package backupsource adapts typed Bria state into sanitized semantic backups.
package backupsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"bria/internal/backupruntime"
	"bria/internal/computer"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/settings"
)

var (
	ErrInvalidSource                    = errors.New("invalid backup source")
	ErrAcceptedInputNeedsReconciliation = errors.New("accepted input must be reconciled before backup")
)

type ReadBarrier interface {
	BeginRead(context.Context) (func() error, error)
}
type SettingsSource interface {
	Current(context.Context) (settings.Snapshot, error)
}
type ComputerSource interface {
	Snapshot() computer.CatalogSnapshot
}
type SessionSource interface {
	List(context.Context) ([]domain.Session, error)
}
type JournalSource interface {
	Inputs(context.Context, string) ([]messagejournal.Input, error)
	Outputs(context.Context, string) ([]messagejournal.Output, error)
}
type HistorySource interface {
	OpenHistory(context.Context, domain.SessionSnapshot) (io.ReadCloser, error)
}

type Options struct {
	Barrier   ReadBarrier
	Settings  SettingsSource
	Computers ComputerSource
	Sessions  SessionSource
	Journal   JournalSource
	Histories map[domain.Provider]HistorySource
	Harness   backupruntime.TreeSource
}

type CurrentState struct{ options Options }

func New(options Options) (*CurrentState, error) {
	if options.Barrier == nil || options.Settings == nil || options.Computers == nil || options.Sessions == nil || options.Journal == nil || options.Harness == nil || options.Histories[domain.ProviderCodex] == nil || options.Histories[domain.ProviderClaude] == nil {
		return nil, fmt.Errorf("%w: all typed sources and both provider histories are required", ErrInvalidSource)
	}
	options.Histories = map[domain.Provider]HistorySource{
		domain.ProviderCodex:  options.Histories[domain.ProviderCodex],
		domain.ProviderClaude: options.Histories[domain.ProviderClaude],
	}
	return &CurrentState{options: options}, nil
}

func (source *CurrentState) BeginSnapshot(ctx context.Context) (_ backupruntime.SnapshotTransaction, returnErr error) {
	if source == nil || ctx == nil {
		return nil, ErrInvalidSource
	}
	release, err := source.options.Barrier.BeginRead(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin state read barrier: %w", err)
	}
	if release == nil {
		return nil, fmt.Errorf("%w: read barrier returned no release", ErrInvalidSource)
	}
	failed := true
	defer func() {
		if failed {
			if releaseErr := release(); releaseErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("release state read barrier: %w", releaseErr))
			}
		}
	}()

	settingSnapshot, err := source.options.Settings.Current(ctx)
	validationErr := settingSnapshot.Settings.Validate()
	if err != nil || validationErr != nil {
		return nil, fmt.Errorf("read settings snapshot: %w", errors.Join(err, validationErr))
	}
	computerSnapshot := source.options.Computers.Snapshot()
	if _, err := computer.RestoreCatalog(computerSnapshot); err != nil {
		return nil, fmt.Errorf("validate computer snapshot: %w", err)
	}
	sessions, err := source.options.Sessions.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("read session snapshot: %w", err)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
	sessionSnapshots := make([]domain.SessionSnapshot, len(sessions))
	undelivered := UndeliveredState{Version: 1, Sessions: make([]UndeliveredSession, 0, len(sessions))}
	seenSessions := make(map[domain.SessionID]struct{}, len(sessions))
	for index, session := range sessions {
		snapshot := session.Snapshot()
		if _, duplicate := seenSessions[snapshot.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate session %q", ErrInvalidSource, snapshot.ID)
		}
		if _, err := domain.RestoreSession(snapshot); err != nil {
			return nil, fmt.Errorf("%w: invalid session snapshot: %v", ErrInvalidSource, err)
		}
		seenSessions[snapshot.ID] = struct{}{}
		sessionSnapshots[index] = snapshot
		inputs, err := source.options.Journal.Inputs(ctx, string(session.ID()))
		if err != nil {
			return nil, fmt.Errorf("read undelivered input for session %q: %w", session.ID(), err)
		}
		outputs, err := source.options.Journal.Outputs(ctx, string(session.ID()))
		if err != nil {
			return nil, fmt.Errorf("read undelivered output for session %q: %w", session.ID(), err)
		}
		if err := validateJournalRecords(session.ID(), inputs, outputs); err != nil {
			return nil, err
		}
		for _, input := range inputs {
			if input.Phase == messagejournal.InputAccepted {
				return nil, fmt.Errorf("%w: session %q message %q sequence %d", ErrAcceptedInputNeedsReconciliation, session.ID(), input.MessageID, input.Sequence)
			}
		}
		filtered := filterUndelivered(session.ID(), inputs, outputs)
		if len(filtered.Records) > 0 {
			undelivered.Sessions = append(undelivered.Sessions, filtered)
		}
	}
	documents, err := marshalDocuments(settingSnapshot, computerSnapshot, sessionSnapshots, undelivered)
	if err != nil {
		return nil, err
	}
	failed = false
	return &transaction{release: release, sources: backupruntime.Sources{
		Settings: document(documents.settings), Computers: document(documents.computers),
		Sessions: document(documents.sessions), UndeliveredMessages: document(documents.undelivered),
		TextHistory: historyTree{sessions: sessionSnapshots, sources: source.options.Histories}, Harness: source.options.Harness,
	}}, nil
}

type transaction struct {
	release func() error
	sources backupruntime.Sources
	closed  bool
}

func (t *transaction) Sources() backupruntime.Sources { return t.sources }
func (t *transaction) Close() error {
	if t.closed {
		return errors.New("backup snapshot transaction already closed")
	}
	t.closed = true
	return t.release()
}

type document []byte

func (d document) Export(_ context.Context, writer io.Writer) error {
	_, err := io.Copy(writer, bytes.NewReader(d))
	return err
}

type historyTree struct {
	sessions []domain.SessionSnapshot
	sources  map[domain.Provider]HistorySource
}

func (tree historyTree) Export(ctx context.Context, sink backupruntime.TreeSink) error {
	for _, session := range tree.sessions {
		source := tree.sources[session.Provider]
		reader, err := source.OpenHistory(ctx, session)
		if err != nil {
			return fmt.Errorf("open full %s history for %q: %w", session.Provider, session.ID, err)
		}
		name, err := historyName(session)
		if err == nil {
			err = sink.WriteFile(name, reader)
		}
		closeErr := reader.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func historyName(session domain.SessionSnapshot) (string, error) {
	id := string(session.ID)
	if id == "" || path.Base(id) != id || strings.ContainsAny(id, `/\\\x00`) {
		return "", fmt.Errorf("%w: unsafe session history id", ErrInvalidSource)
	}
	return path.Join(string(session.Provider), id+".jsonl"), nil
}

type documents struct{ settings, computers, sessions, undelivered []byte }

func marshalDocuments(setting settings.Snapshot, computers computer.CatalogSnapshot, sessions []domain.SessionSnapshot, undelivered UndeliveredState) (documents, error) {
	values := []any{
		settingsDocument{Version: 1, Revision: setting.Revision, Settings: setting.Settings},
		computerDocument{Version: 1, Computers: computers},
		sessionDocument{Version: 1, Sessions: sessions},
		undelivered,
	}
	encoded := make([][]byte, len(values))
	for i, value := range values {
		var err error
		encoded[i], err = json.Marshal(value)
		if err != nil {
			return documents{}, err
		}
	}
	return documents{encoded[0], encoded[1], encoded[2], encoded[3]}, nil
}

type UndeliveredState struct {
	Version  int                  `json:"version"`
	Sessions []UndeliveredSession `json:"sessions"`
}
type UndeliveredSession struct {
	SessionID domain.SessionID    `json:"session_id"`
	Records   []UndeliveredRecord `json:"records"`
}

// UndeliveredRecord is an ordered semantic queue item, not a raw journal
// record. SourceSequence preserves relative input/output order and may contain
// gaps left by delivered records. RestoreTarget must enqueue Records in this
// order into a fresh journal, assigning new contiguous durable sequences.
type UndeliveredRecord struct {
	SourceSequence uint64             `json:"source_sequence"`
	Input          *UndeliveredInput  `json:"input,omitempty"`
	Output         *UndeliveredOutput `json:"output,omitempty"`
}
type UndeliveredInput struct {
	MessageID string                    `json:"message_id"`
	Payload   []byte                    `json:"payload"`
	Phase     messagejournal.InputPhase `json:"phase"`
}
type UndeliveredOutput struct {
	OperationID string                     `json:"operation_id"`
	Kind        string                     `json:"kind"`
	Payload     []byte                     `json:"payload"`
	Phase       messagejournal.OutputPhase `json:"phase"`
}

func filterUndelivered(id domain.SessionID, inputs []messagejournal.Input, outputs []messagejournal.Output) UndeliveredSession {
	result := UndeliveredSession{SessionID: id, Records: []UndeliveredRecord{}}
	for _, input := range inputs {
		if input.Phase == messagejournal.InputPending || input.Phase == messagejournal.InputFailed || input.Phase == messagejournal.InputUnknown {
			item := &UndeliveredInput{MessageID: input.MessageID, Payload: append([]byte(nil), input.Payload...), Phase: input.Phase}
			result.Records = append(result.Records, UndeliveredRecord{SourceSequence: input.Sequence, Input: item})
		}
	}
	for _, output := range outputs {
		if output.Phase == messagejournal.OutputPending || output.Phase == messagejournal.OutputFailed || output.Phase == messagejournal.OutputUnknown {
			item := &UndeliveredOutput{OperationID: output.OperationID, Kind: output.Kind, Payload: append([]byte(nil), output.Payload...), Phase: output.Phase}
			result.Records = append(result.Records, UndeliveredRecord{SourceSequence: output.Sequence, Output: item})
		}
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].SourceSequence < result.Records[j].SourceSequence })
	return result
}

func validateJournalRecords(id domain.SessionID, inputs []messagejournal.Input, outputs []messagejournal.Output) error {
	sequences := make(map[uint64]struct{}, len(inputs)+len(outputs))
	inputIDs := make(map[string]struct{}, len(inputs))
	outputIDs := make(map[string]struct{}, len(outputs))
	for _, input := range inputs {
		if input.SessionID != string(id) || strings.TrimSpace(input.MessageID) != input.MessageID || input.MessageID == "" || input.Sequence == 0 || !validInputPhase(input.Phase) {
			return fmt.Errorf("%w: invalid journal input for session %q", ErrInvalidSource, id)
		}
		if _, duplicate := inputIDs[input.MessageID]; duplicate {
			return fmt.Errorf("%w: duplicate journal input identity", ErrInvalidSource)
		}
		if _, duplicate := sequences[input.Sequence]; duplicate {
			return fmt.Errorf("%w: duplicate journal sequence", ErrInvalidSource)
		}
		inputIDs[input.MessageID] = struct{}{}
		sequences[input.Sequence] = struct{}{}
	}
	for _, output := range outputs {
		if output.SessionID != string(id) || strings.TrimSpace(output.OperationID) != output.OperationID || output.OperationID == "" || strings.TrimSpace(output.Kind) != output.Kind || output.Kind == "" || output.Sequence == 0 || !validOutputPhase(output.Phase) {
			return fmt.Errorf("%w: invalid journal output for session %q", ErrInvalidSource, id)
		}
		if _, duplicate := outputIDs[output.OperationID]; duplicate {
			return fmt.Errorf("%w: duplicate journal output identity", ErrInvalidSource)
		}
		if _, duplicate := sequences[output.Sequence]; duplicate {
			return fmt.Errorf("%w: duplicate journal sequence", ErrInvalidSource)
		}
		outputIDs[output.OperationID] = struct{}{}
		sequences[output.Sequence] = struct{}{}
	}
	return nil
}

func validInputPhase(phase messagejournal.InputPhase) bool {
	switch phase {
	case messagejournal.InputPending, messagejournal.InputAccepted, messagejournal.InputCompleted, messagejournal.InputFailed, messagejournal.InputUnknown:
		return true
	default:
		return false
	}
}

func validOutputPhase(phase messagejournal.OutputPhase) bool {
	switch phase {
	case messagejournal.OutputPending, messagejournal.OutputConfirmed, messagejournal.OutputFailed, messagejournal.OutputUnknown:
		return true
	default:
		return false
	}
}
