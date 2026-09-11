package telegrambridge

import (
	"bria/internal/callbacktoken"
	"bria/internal/telegram"
	"bria/internal/telegramcallbackview"
	"bria/internal/telegramrecovery"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramui"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxButtonsPerRow   = 8
	maxKeyboardButtons = 100
	maxButtonTextRunes = 64
)

type Callback struct {
	SessionID string
	Action    telegramui.Action
	Target    telegramui.ButtonTarget
}

type DecodedCallback struct {
	Callback  Callback
	TokenID   string
	ExpiresAt time.Time
}

type KeyboardPresentation struct {
	Markup               telegram.InlineKeyboardMarkup
	SessionID            string
	TokenIDs             []string
	ExpiresAt            time.Time
	InteractionRequestID string
	OutboundOperationID  string
	OutboundUpdateID     int64
	Recovery             *CallbackRecoveryBinding
	AcceptedTurnRecovery *AcceptedTurnRecoveryBinding
	StatusRecovery       *StatusRecoveryBinding
	ArtifactRetry        *ArtifactRetryBinding
}
type CallbackRecoveryBinding struct {
	OperationID string
	UpdateID    int64
	SessionID   string
	ChatID      int64
	MessageID   int64
	Phase       string
}
type AcceptedTurnRecoveryBinding = telegramrecovery.AcceptedTurnBinding
type StatusRecoveryBinding = statusrecovery.Binding
type ArtifactRetryBinding struct {
	PresentationID, SessionID, MessageID, FinalOperationID string
	Generation                                             uint64
	Slot                                                   uint16
	ExpiresAt                                              time.Time
}

type PresentedNotification struct {
	Text string
	KeyboardPresentation
}

type Presenter struct {
	codec *callbacktoken.Codec
	now   func() time.Time
	ttl   time.Duration
}

func NewPresenter(codec *callbacktoken.Codec, now func() time.Time, ttl time.Duration) (*Presenter, error) {
	if codec == nil {
		return nil, errors.New("callback token codec is required")
	}
	if now == nil {
		return nil, errors.New("callback token clock is required")
	}
	if ttl < time.Second {
		return nil, errors.New("callback token TTL must be at least one second")
	}
	return &Presenter{codec: codec, now: now, ttl: ttl}, nil
}

func (presenter *Presenter) PresentKeyboard(
	logicalSessionID string,
	selectableSessionIDs []string,
	keyboard telegramui.CardKeyboard,
) (telegram.InlineKeyboardMarkup, error) {
	return presenter.presentKeyboard(logicalSessionID, selectableSessionIDs, keyboard, false, false, false, false, false, false)
}
func (presenter *Presenter) presentKeyboard(
	logicalSessionID string,
	selectableSessionIDs []string,
	keyboard telegramui.CardKeyboard,
	allowInteraction bool,
	allowOutboundResolution bool,
	allowCallbackRecovery bool,
	allowAcceptedTurnRecovery bool,
	allowStatusRecovery bool,
	allowArtifactRetry bool,
) (telegram.InlineKeyboardMarkup, error) {
	if !callbacktoken.IsCanonicalSessionID(logicalSessionID) {
		return telegram.InlineKeyboardMarkup{}, errors.New("owning logical session ID must be a canonical UUID")
	}
	if len(keyboard.Rows) == 0 {
		return telegram.InlineKeyboardMarkup{}, errors.New("Telegram keyboard must contain at least one row")
	}
	totalButtons := 0
	selectableSlots := 0
	for rowIndex, row := range keyboard.Rows {
		if len(row) == 0 || len(row) > maxButtonsPerRow {
			return telegram.InlineKeyboardMarkup{}, fmt.Errorf(
				"Telegram keyboard row %d must contain 1..%d buttons",
				rowIndex,
				maxButtonsPerRow,
			)
		}
		totalButtons += len(row)
		if totalButtons > maxKeyboardButtons {
			return telegram.InlineKeyboardMarkup{}, fmt.Errorf(
				"Telegram keyboard must contain at most %d buttons",
				maxKeyboardButtons,
			)
		}
		for _, button := range row {
			if telegramui.IsArtifactRetryAction(button.Action) != allowArtifactRetry {
				return telegram.InlineKeyboardMarkup{}, errors.New("artifact retry button requires an exact delivery binding")
			}
			if telegramui.IsStatusRecoveryAction(button.Action) && !allowStatusRecovery {
				return telegram.InlineKeyboardMarkup{}, errors.New("status recovery button requires an exact operation binding")
			}
			if allowStatusRecovery && !telegramui.IsStatusRecoveryAction(button.Action) {
				return telegram.InlineKeyboardMarkup{}, errors.New("status recovery keyboard must contain only recovery actions")
			}
			if telegramui.IsAcceptedTurnRecoveryAction(button.Action) && !allowAcceptedTurnRecovery {
				return telegram.InlineKeyboardMarkup{}, errors.New("accepted-turn recovery button requires an exact turn binding")
			}
			if allowAcceptedTurnRecovery && !telegramui.IsAcceptedTurnRecoveryAction(button.Action) {
				return telegram.InlineKeyboardMarkup{}, errors.New("accepted-turn recovery keyboard must contain only recovery actions")
			}
			if telegramui.IsInteractionAction(button.Action) && !allowInteraction {
				return telegram.InlineKeyboardMarkup{}, errors.New("interaction button requires an opaque request binding")
			}
			if allowInteraction && !telegramui.IsInteractionAction(button.Action) {
				return telegram.InlineKeyboardMarkup{}, errors.New("interaction keyboard must contain only interaction actions")
			}
			if telegramui.IsOutboundResolutionAction(button.Action) && !allowOutboundResolution {
				return telegram.InlineKeyboardMarkup{}, errors.New("outbound resolution button requires an exact operation binding")
			}
			if allowOutboundResolution && !telegramui.IsOutboundResolutionAction(button.Action) {
				return telegram.InlineKeyboardMarkup{}, errors.New("outbound resolution keyboard must contain only resolution actions")
			}
			if telegramui.IsCallbackRecoveryAction(button.Action) && !allowCallbackRecovery {
				return telegram.InlineKeyboardMarkup{}, errors.New("callback recovery button requires an exact operation binding")
			}
			if allowCallbackRecovery && !telegramui.IsCallbackRecoveryAction(button.Action) {
				return telegram.InlineKeyboardMarkup{}, errors.New("callback recovery keyboard must contain only recovery actions")
			}
			if button.Action != telegramui.ActionSelectSession &&
				!telegramui.IsSessionSurfaceAction(button.Action) &&
				!(button.Action == telegramui.ActionResume && button.Target.SessionSlot > 0) {
				continue
			}
			selectableSlots++
			if button.Target.SessionSlot != selectableSlots {
				return telegram.InlineKeyboardMarkup{}, errors.New("selectable session slots must be unique and ordered from one")
			}
		}
	}
	if len(selectableSessionIDs) != selectableSlots {
		return telegram.InlineKeyboardMarkup{}, errors.New("selectable session IDs must exactly match semantic session slots")
	}
	for _, sessionID := range selectableSessionIDs {
		if !callbacktoken.IsCanonicalSessionID(sessionID) {
			return telegram.InlineKeyboardMarkup{}, errors.New("selectable logical session ID must be a canonical UUID")
		}
	}
	now := presenter.now().UTC()
	expiresAt := now.Add(presenter.ttl).Truncate(time.Second)
	if expiresAt.Unix() <= now.Unix() {
		return telegram.InlineKeyboardMarkup{}, errors.New("callback token TTL does not produce a future expiry")
	}
	rows := make([][]telegram.InlineKeyboardButton, len(keyboard.Rows))
	for rowIndex, row := range keyboard.Rows {
		rows[rowIndex] = make([]telegram.InlineKeyboardButton, len(row))
		for buttonIndex, button := range row {
			label, action, target, err := telegramcallbackview.PresentButton(button)
			if err != nil {
				return telegram.InlineKeyboardMarkup{}, fmt.Errorf(
					"present Telegram keyboard button %d:%d: %w",
					rowIndex,
					buttonIndex,
					err,
				)
			}
			if !utf8.ValidString(label) || utf8.RuneCountInString(label) < 1 ||
				utf8.RuneCountInString(label) > maxButtonTextRunes {
				return telegram.InlineKeyboardMarkup{}, fmt.Errorf(
					"Telegram keyboard button %d:%d text must contain 1..%d characters",
					rowIndex,
					buttonIndex,
					maxButtonTextRunes,
				)
			}
			tokenSessionID := logicalSessionID
			// Nodes is a global surface, but when it is opened from a session
			// card its signed callback must retain that card as the return target.
			// Global menu/sessions surfaces already use GlobalSurfaceID here.
			if telegramui.IsGlobalAction(button.Action) &&
				!(button.Action == telegramui.ActionMenuNodes && logicalSessionID != telegramui.GlobalSurfaceID) {
				tokenSessionID = telegramui.GlobalSurfaceID
			} else if button.Action == telegramui.ActionSelectSession ||
				telegramui.IsSessionSurfaceAction(button.Action) ||
				(button.Action == telegramui.ActionResume && button.Target.SessionSlot > 0) {
				tokenSessionID = selectableSessionIDs[button.Target.SessionSlot-1]
			}
			token, err := presenter.codec.Encode(callbacktoken.Fields{
				Action:    action,
				SessionID: tokenSessionID,
				Target:    target,
				ExpiresAt: expiresAt,
			})
			if err != nil {
				return telegram.InlineKeyboardMarkup{}, fmt.Errorf(
					"encode Telegram keyboard button %d:%d callback: %w",
					rowIndex,
					buttonIndex,
					err,
				)
			}
			if len(token) < 1 || len(token) > callbacktoken.EncodedLength {
				return telegram.InlineKeyboardMarkup{}, fmt.Errorf(
					"Telegram keyboard button %d:%d callback_data must contain 1..64 bytes",
					rowIndex,
					buttonIndex,
				)
			}
			rows[rowIndex][buttonIndex] = telegram.InlineKeyboardButton{
				Text:         label,
				CallbackData: token,
			}
		}
	}
	return telegram.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func (presenter *Presenter) PresentKeyboardWithManifest(
	logicalSessionID string,
	selectableSessionIDs []string,
	keyboard telegramui.CardKeyboard,
) (KeyboardPresentation, error) {
	markup, err := presenter.PresentKeyboard(logicalSessionID, selectableSessionIDs, keyboard)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	return presenter.presentationFromMarkup(logicalSessionID, "", markup)
}

func (presenter *Presenter) PresentInteractionKeyboardWithManifest(
	logicalSessionID string,
	interactionRequestID string,
	keyboard telegramui.CardKeyboard,
) (KeyboardPresentation, error) {
	if interactionRequestID == "" || len(interactionRequestID) > 256 || !utf8.ValidString(interactionRequestID) {
		return KeyboardPresentation{}, errors.New("interaction request identity is invalid")
	}
	markup, err := presenter.presentKeyboard(logicalSessionID, nil, keyboard, true, false, false, false, false, false)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	return presenter.presentationFromMarkup(logicalSessionID, interactionRequestID, markup)
}

func (presenter *Presenter) PresentOutboundResolutionKeyboardWithManifest(
	operationID string,
	updateID int64,
	keyboard telegramui.CardKeyboard,
) (KeyboardPresentation, error) {
	if operationID == "" || len(operationID) > 256 || !utf8.ValidString(operationID) || updateID <= 0 {
		return KeyboardPresentation{}, errors.New("outbound resolution identity is invalid")
	}
	markup, err := presenter.presentKeyboard(telegramui.GlobalSurfaceID, nil, keyboard, false, true, false, false, false, false)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	presentation, err := presenter.presentationFromMarkup(telegramui.GlobalSurfaceID, "", markup)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	presentation.OutboundOperationID = operationID
	presentation.OutboundUpdateID = updateID
	return presentation, nil
}
func (presenter *Presenter) PresentCallbackRecoveryKeyboardWithManifest(
	binding CallbackRecoveryBinding,
	keyboard telegramui.CardKeyboard,
) (KeyboardPresentation, error) {
	if binding.OperationID == "" || len(binding.OperationID) > 256 || !utf8.ValidString(binding.OperationID) ||
		binding.UpdateID <= 0 || !callbacktoken.IsCanonicalSessionID(binding.SessionID) || binding.ChatID <= 0 || binding.MessageID <= 0 ||
		(binding.Phase != "effect_unknown" && binding.Phase != "effect_retry_unknown" && binding.Phase != "send_unknown") {
		return KeyboardPresentation{}, errors.New("callback recovery identity is invalid")
	}
	markup, err := presenter.presentKeyboard(telegramui.GlobalSurfaceID, nil, keyboard, false, false, true, false, false, false)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	presentation, err := presenter.presentationFromMarkup(telegramui.GlobalSurfaceID, "", markup)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	copyBinding := binding
	presentation.Recovery = &copyBinding
	return presentation, nil
}
func (presenter *Presenter) PresentAcceptedTurnRecoveryKeyboardWithManifest(
	binding AcceptedTurnRecoveryBinding,
	keyboard telegramui.CardKeyboard,
) (KeyboardPresentation, error) {
	if !telegramrecovery.ValidAcceptedTurnBinding(&binding) || !callbacktoken.IsCanonicalSessionID(string(binding.SessionID)) {
		return KeyboardPresentation{}, errors.New("accepted-turn recovery identity is invalid")
	}
	markup, err := presenter.presentKeyboard(string(binding.SessionID), nil, keyboard, false, false, false, true, false, false)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	presentation, err := presenter.presentationFromMarkup(string(binding.SessionID), "", markup)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	copyBinding := binding
	presentation.AcceptedTurnRecovery = &copyBinding
	return presentation, nil
}
func (presenter *Presenter) PresentStatusRecoveryKeyboardWithManifest(
	binding StatusRecoveryBinding,
	keyboard telegramui.CardKeyboard,
) (KeyboardPresentation, error) {
	if !statusrecovery.Valid(binding) {
		return KeyboardPresentation{}, errors.New("status recovery identity is invalid")
	}
	markup, err := presenter.presentKeyboard(telegramui.GlobalSurfaceID, nil, keyboard, false, false, false, false, true, false)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	presentation, err := presenter.presentationFromMarkup(telegramui.GlobalSurfaceID, "", markup)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	copyBinding := binding
	presentation.StatusRecovery = &copyBinding
	return presentation, nil
}
func (presenter *Presenter) PresentArtifactRetryKeyboardWithManifest(binding ArtifactRetryBinding, keyboard telegramui.CardKeyboard) (KeyboardPresentation, error) {
	if !validArtifactRetryBinding(&binding) || !binding.ExpiresAt.After(presenter.now().UTC()) {
		return KeyboardPresentation{}, errors.New("artifact retry identity is invalid")
	}
	limited := *presenter
	if remaining := binding.ExpiresAt.Sub(presenter.now().UTC()); remaining < limited.ttl {
		limited.ttl = remaining
	}
	markup, err := limited.presentKeyboard(binding.PresentationID, nil, keyboard, false, false, false, false, false, true)
	if err != nil {
		return KeyboardPresentation{}, err
	}
	presentation, err := presenter.presentationFromMarkup(binding.PresentationID, "", markup)
	if err != nil || presentation.ExpiresAt.After(binding.ExpiresAt) {
		return KeyboardPresentation{}, errors.New("artifact retry callback expiry is invalid")
	}
	copyBinding := binding
	presentation.ArtifactRetry = &copyBinding
	return presentation, nil
}

func validArtifactRetryBinding(binding *ArtifactRetryBinding) bool {
	return binding != nil && callbacktoken.IsCanonicalSessionID(binding.PresentationID) && callbacktoken.IsCanonicalSessionID(binding.SessionID) &&
		binding.PresentationID != binding.SessionID && binding.MessageID != "" && len(binding.MessageID) <= 1024 &&
		binding.FinalOperationID == binding.MessageID+":final" && binding.Generation > 0 && binding.Slot > 0 &&
		binding.ExpiresAt.Unix() > 0 && binding.ExpiresAt.Nanosecond() == 0
}
func (presenter *Presenter) presentationFromMarkup(logicalSessionID, interactionRequestID string, markup telegram.InlineKeyboardMarkup) (KeyboardPresentation, error) {
	result := KeyboardPresentation{
		Markup: markup, SessionID: logicalSessionID,
		TokenIDs: make([]string, 0, countButtons(markup)), InteractionRequestID: interactionRequestID,
	}
	seen := make(map[string]struct{}, countButtons(markup))
	for rowIndex, row := range markup.InlineKeyboard {
		for buttonIndex, button := range row {
			decoded, err := presenter.DecodeCallbackWithMetadata(button.CallbackData)
			if err != nil {
				return KeyboardPresentation{}, fmt.Errorf(
					"decode presented Telegram keyboard button %d:%d: %w",
					rowIndex,
					buttonIndex,
					err,
				)
			}
			if result.ExpiresAt.IsZero() {
				result.ExpiresAt = decoded.ExpiresAt
			} else if result.ExpiresAt != decoded.ExpiresAt {
				return KeyboardPresentation{}, errors.New("presented callback expiries are inconsistent")
			}
			if _, duplicate := seen[decoded.TokenID]; duplicate {
				return KeyboardPresentation{}, errors.New("presented callback token identities must be unique")
			}
			seen[decoded.TokenID] = struct{}{}
			result.TokenIDs = append(result.TokenIDs, decoded.TokenID)
		}
	}
	return result, nil
}

func (presenter *Presenter) PresentBackgroundCompletion(logicalSessionID, sessionName string) (PresentedNotification, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return PresentedNotification{}, errors.New("background completion session name is required")
	}
	presentation, err := presenter.PresentKeyboardWithManifest(
		logicalSessionID,
		[]string{logicalSessionID},
		telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{
			Action: telegramui.ActionSelectSession,
			Target: telegramui.ButtonTarget{SessionSlot: 1},
		}}}},
	)
	if err != nil {
		return PresentedNotification{}, err
	}
	presentation.Markup.InlineKeyboard[0][0].Text = "Открыть"
	return PresentedNotification{
		Text:                 fmt.Sprintf("Фоновая сессия «%s» завершена.", sessionName),
		KeyboardPresentation: presentation,
	}, nil
}

func (presenter *Presenter) DecodeCallback(callbackData string) (Callback, error) {
	decoded, err := presenter.DecodeCallbackWithMetadata(callbackData)
	if err != nil {
		return Callback{}, err
	}
	return decoded.Callback, nil
}

func (presenter *Presenter) DecodeCallbackWithMetadata(callbackData string) (DecodedCallback, error) {
	fields, err := presenter.codec.Decode(callbackData)
	if err != nil {
		return DecodedCallback{}, fmt.Errorf("decode Telegram callback: %w", err)
	}
	action, target, err := telegramcallbackview.DecodeFields(fields)
	if err != nil {
		return DecodedCallback{}, err
	}
	return DecodedCallback{
		Callback: Callback{
			SessionID: fields.SessionID,
			Action:    action,
			Target:    target,
		},
		TokenID:   callbackTokenID(callbackData),
		ExpiresAt: fields.ExpiresAt,
	}, nil
}

func (presenter *Presenter) PresentationCurrentlyValid(presentation KeyboardPresentation) bool {
	if presenter == nil || len(presentation.TokenIDs) == 0 {
		return false
	}
	index := 0
	for _, row := range presentation.Markup.InlineKeyboard {
		for _, button := range row {
			if index >= len(presentation.TokenIDs) {
				return false
			}
			decoded, err := presenter.DecodeCallbackWithMetadata(button.CallbackData)
			if err != nil || decoded.TokenID != presentation.TokenIDs[index] || decoded.ExpiresAt != presentation.ExpiresAt {
				return false
			}
			index++
		}
	}
	return index == len(presentation.TokenIDs)
}
func callbackTokenID(callbackData string) string {
	digest := sha256.Sum256([]byte(callbackData))
	return base64.RawURLEncoding.EncodeToString(digest[:16])
}
func countButtons(markup telegram.InlineKeyboardMarkup) int {
	total := 0
	for _, row := range markup.InlineKeyboard {
		total += len(row)
	}
	return total
}
