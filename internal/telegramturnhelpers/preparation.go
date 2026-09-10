// Package telegramturnhelpers prepares Telegram input for exact provider turns.
// It owns media normalization, prompt preprocessing and durable admission checks;
// controller lifecycle, scheduling and delivery remain with the caller.
package telegramturnhelpers

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/settingsport"
	"bria/internal/turnprocessing"
)

// Preparation has immutable ports/options; each call owns its request state.
type Preparation struct {
	Settings           settingsport.Preferences
	Processor          promptpreprocess.Processor
	Observer           promptpreprocess.Observer
	Timeout            time.Duration
	InputPreparer      turnprocessing.InputPreparer
	AllowDocumentInput bool
}

// Invalidate drops cached preprocessing selection for the changed node settings.
func (p Preparation) Invalidate(node domain.ComputerID) {
	if invalidator, ok := p.Processor.(promptpreprocess.Invalidator); ok {
		invalidator.Invalidate(node)
	}
}

// Reconfigure invalidates provider selection and then restores the topology
// selected by the just-persisted settings document.
func (p Preparation) Reconfigure(ctx context.Context, node domain.ComputerID) error {
	p.Invalidate(node)
	controller, ok := p.Processor.(promptpreprocess.ModeController)
	if !ok || p.Settings == nil {
		return nil
	}
	snapshot, err := p.Settings.Snapshot(ctx)
	if err != nil {
		return err
	}
	return controller.SetMode(ctx, preprocessingMode(snapshot))
}

func MediaPromptLabel(kind string) string {
	switch kind {
	case "voice":
		return "Голосовое сообщение"
	case "photo":
		return "Фотография"
	case "video":
		return "Видео"
	case "document":
		return "Документ"
	default:
		return "Сообщение"
	}
}

func (p Preparation) Payload(ctx context.Context, text string) []byte {
	text = strings.TrimSpace(text)
	if text == "" || p.Settings == nil {
		return []byte(text)
	}
	snapshot, err := p.Settings.Snapshot(ctx)
	if err != nil || promptpreprocess.Bypass(text) {
		return []byte(text)
	}
	mode := preprocessingMode(snapshot)
	payload, err := promptpreprocess.EncodeMode(mode, snapshot.PreprocessingInstruction, text)
	if err != nil {
		return []byte(text)
	}
	return payload
}

func (p Preparation) Process(ctx context.Context, session domain.Session, messageID string, sequence uint64, payload []byte) (string, bool, []byte, promptpreprocess.Completion) {
	state, err := promptpreprocess.DecodeState(payload)
	if err != nil {
		p.observeFailure(ctx, session, messageID, promptpreprocess.Result{}, "decode", "invalid_input", err)
		return strings.TrimSpace(string(payload)), true, nil, nil
	}
	if !state.Enabled {
		return strings.TrimSpace(state.Original), false, nil, nil
	}
	if state.Prepared {
		if p.Observer != nil {
			_ = p.Observer.ObservePreprocessing(context.WithoutCancel(ctx), promptpreprocess.CachedObservation(promptpreprocess.Request{ComputerID: session.ComputerID(), SessionID: session.ID(), MessageID: messageID, Mode: state.Mode}, state.Failed))
		}
		return state.Processed, state.Failed, nil, nil
	}
	if p.Processor == nil {
		err = errors.New("preprocessor is unavailable")
		p.observeFailure(ctx, session, messageID, promptpreprocess.Result{}, "select", "unavailable", err)
		prepared, prepareErr := promptpreprocess.MarkPrepared(payload, state.Original, true)
		if prepareErr != nil {
			p.observeFailure(ctx, session, messageID, promptpreprocess.Result{}, "prepare", "invalid_state", prepareErr)
			return state.Original, true, nil, nil
		}
		return state.Original, true, prepared, nil
	}
	preprocessContext, cancel := context.WithTimeout(ctx, p.Timeout)
	result, processErr := p.Processor.Process(preprocessContext, promptpreprocess.Request{
		ComputerID: session.ComputerID(), SessionID: session.ID(), MessageID: messageID,
		Sequence: sequence, Mode: state.Mode, Instruction: state.Instruction, Text: state.Original,
	})
	cancel()
	completion := result.Completion
	var validationErr error
	if processErr == nil {
		validationErr = promptpreprocess.ValidateResult(state.Original, result.Text)
		processErr = validationErr
	}
	if processErr != nil {
		category := "provider"
		stage := "invoke"
		if errors.Is(processErr, context.DeadlineExceeded) || errors.Is(preprocessContext.Err(), context.DeadlineExceeded) {
			category = "timeout"
		} else if validationErr != nil {
			category = "invalid_output"
			stage = "validate"
		}
		p.observeFailure(ctx, session, messageID, result, stage, category, processErr)
		prepared, prepareErr := promptpreprocess.MarkPrepared(payload, state.Original, true)
		if prepareErr != nil {
			p.observeFailure(ctx, session, messageID, result, "prepare", "invalid_state", prepareErr)
			return state.Original, true, nil, completion
		}
		return state.Original, true, prepared, completion
	}
	cleaned := strings.TrimSpace(result.Text)
	completion = p.acceptedCompletion(promptpreprocess.Request{
		ComputerID: session.ComputerID(), SessionID: session.ID(), MessageID: messageID, Sequence: sequence, Mode: state.Mode,
	}, result, completion)
	prepared, prepareErr := promptpreprocess.MarkPrepared(payload, cleaned, false)
	if prepareErr != nil {
		p.observeFailure(ctx, session, messageID, result, "prepare", "invalid_state", prepareErr)
		return state.Original, true, nil, completion
	}
	return cleaned, false, prepared, completion
}

func preprocessingMode(snapshot settingsport.Snapshot) promptpreprocess.Mode {
	switch snapshot.SatellitePreprocessingMode {
	case settingsport.SatellitePreprocessingDisabled:
		return promptpreprocess.ModeDisabled
	case settingsport.SatellitePreprocessingShared:
		return promptpreprocess.ModeShared
	case settingsport.SatellitePreprocessingPerSession:
		return promptpreprocess.ModePerSession
	case "":
		if snapshot.PreprocessingEnabled {
			return promptpreprocess.ModeShared
		}
	}
	return promptpreprocess.ModeDisabled
}

func (p Preparation) acceptedCompletion(request promptpreprocess.Request, result promptpreprocess.Result, inner promptpreprocess.Completion) promptpreprocess.Completion {
	if p.Observer == nil {
		return inner
	}
	return &acceptedPreprocessingCompletion{observer: p.Observer, request: request, result: result, inner: inner}
}

type acceptedPreprocessingCompletion struct {
	once     sync.Once
	observer promptpreprocess.Observer
	request  promptpreprocess.Request
	result   promptpreprocess.Result
	inner    promptpreprocess.Completion
	err      error
}

func (completion *acceptedPreprocessingCompletion) Accept(ctx context.Context) error {
	completion.once.Do(func() {
		_ = completion.observer.ObservePreprocessing(context.WithoutCancel(ctx), promptpreprocess.SuccessObservation(completion.request, completion.result))
		if completion.inner != nil {
			completion.err = completion.inner.Accept(ctx)
		}
	})
	return completion.err
}

func (p Preparation) observeFailure(ctx context.Context, session domain.Session, messageID string, result promptpreprocess.Result, stage, category string, processErr error) {
	if p.Observer == nil {
		return
	}
	_ = p.Observer.ObservePreprocessing(context.WithoutCancel(ctx), promptpreprocess.Observation{
		ComputerID: session.ComputerID(), SessionID: session.ID(), MessageID: messageID,
		Provider: result.Provider, Model: result.Model, ModelEvidence: result.ModelEvidence, Stage: stage, Category: category,
		Attempts: 1, Error: processErr.Error(),
	})
}

func (p Preparation) Prepare(ctx context.Context, update coordinator.Update) (turnprocessing.PreparedInput, string) {
	base := JoinPromptParts(update.Text, update.Caption)
	if update.MediaKind == "" {
		if base == "" {
			return turnprocessing.PreparedInput{}, "В сообщении нет текста или поддерживаемого вложения."
		}
		return turnprocessing.PreparedInput{Text: base}, ""
	}
	input := turnprocessing.IncomingInput{
		Kind: update.MediaKind, FileID: update.MediaFileID,
		FileUniqueID: update.MediaFileUniqueID, FileSize: update.MediaFileSize,
		MIMEType: update.MediaMIMEType, DurationSeconds: update.MediaDurationSeconds,
		Width: update.MediaWidth, Height: update.MediaHeight,
		DownloadPermitted: update.MediaDownloadAllowed,
	}
	switch input.Kind {
	case "video":
		metadata := "Видео без загрузки"
		if input.DurationSeconds > 0 {
			metadata += fmt.Sprintf(", длительность %d сек.", input.DurationSeconds)
		}
		if input.Width > 0 && input.Height > 0 {
			metadata += fmt.Sprintf(", размер %dx%d", input.Width, input.Height)
		}
		return turnprocessing.PreparedInput{Text: JoinPromptParts(base, metadata)}, ""
	case "document":
		if !p.AllowDocumentInput {
			return turnprocessing.PreparedInput{}, "Обработка документов не разрешена настройками Bria."
		}
	case "voice", "photo":
		if !input.DownloadPermitted {
			return turnprocessing.PreparedInput{}, "Загрузка этого вложения не разрешена."
		}
	default:
		return turnprocessing.PreparedInput{}, "Этот тип вложения пока не поддерживается."
	}
	if p.InputPreparer == nil {
		return turnprocessing.PreparedInput{}, "Обработчик этого вложения не настроен."
	}
	if structured, ok := p.InputPreparer.(turnprocessing.StructuredInputPreparer); ok {
		prepared, err := structured.PrepareStructured(ctx, input)
		if err != nil || ValidatePreparedInput(prepared) != nil {
			return turnprocessing.PreparedInput{}, "Не удалось безопасно подготовить вложение."
		}
		prepared.Text = JoinPromptParts(base, prepared.Text)
		return prepared, ""
	}
	prepared, err := p.InputPreparer.Prepare(ctx, input)
	if err != nil {
		return turnprocessing.PreparedInput{}, "Не удалось безопасно подготовить вложение."
	}
	prepared = strings.TrimSpace(prepared)
	if prepared == "" {
		return turnprocessing.PreparedInput{}, "Вложение не содержит данных для запроса."
	}
	return turnprocessing.PreparedInput{Text: JoinPromptParts(base, prepared)}, ""
}
func ValidatePreparedInput(input turnprocessing.PreparedInput) error {
	if strings.TrimSpace(input.Text) == "" && len(input.Attachments) == 0 {
		return errors.New("prepared input is empty")
	}
	for _, attachment := range input.Attachments {
		if strings.TrimSpace(attachment.Reference) == "" || attachment.Reference != strings.TrimSpace(attachment.Reference) ||
			filepath.IsAbs(attachment.Reference) || strings.ContainsAny(attachment.Reference, `/\\`) || attachment.Size <= 0 || len(attachment.SHA256) != 64 {
			return errors.New("prepared attachment reference is invalid")
		}
		for _, character := range attachment.SHA256 {
			if !strings.ContainsRune("0123456789abcdef", character) {
				return errors.New("prepared attachment digest is invalid")
			}
		}
	}
	return nil
}
func JoinPromptParts(parts ...string) string {
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || (len(result) > 0 && result[len(result)-1] == part) {
			continue
		}
		result = append(result, part)
	}
	return strings.Join(result, "\n\n")
}
func ParseNew(text string) (domain.Provider, string, bool) {
	parts := strings.SplitN(text, " ", 3)
	if len(parts) != 3 || parts[0] != "/new" {
		return "", "", false
	}
	workdir := strings.TrimSpace(parts[2])
	if workdir == "" || !filepath.IsAbs(workdir) {
		return "", "", false
	}
	provider := domain.Provider(parts[1])
	if provider != domain.ProviderCodex && provider != domain.ProviderClaude {
		return "", "", false
	}
	return provider, workdir, true
}
func ParseUse(text string) (domain.SessionID, bool) {
	parts := strings.Fields(text)
	if len(parts) != 2 || parts[0] != "/use" || !canonicalUUID(parts[1]) {
		return "", false
	}
	return domain.SessionID(parts[1]), true
}
func canonicalUUID(value string) bool {
	if len(value) != 36 || value != strings.ToLower(value) ||
		value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
