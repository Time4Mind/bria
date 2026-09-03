package telegrambridge

import (
	"bria/internal/callbacktoken"
	"bria/internal/telegram"
	"bria/internal/telegramrecovery"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramui"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
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
	if !isCanonicalLogicalSessionUUID(logicalSessionID) {
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
		if !isCanonicalLogicalSessionUUID(sessionID) {
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
			label, action, target, err := presentButton(button)
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
			if button.Action == telegramui.ActionSelectSession ||
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
		binding.UpdateID <= 0 || !isCanonicalLogicalSessionUUID(binding.SessionID) || binding.ChatID <= 0 || binding.MessageID <= 0 ||
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
	if !telegramrecovery.ValidAcceptedTurnBinding(&binding) || !isCanonicalLogicalSessionUUID(string(binding.SessionID)) {
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
	return binding != nil && isCanonicalLogicalSessionUUID(binding.PresentationID) && isCanonicalLogicalSessionUUID(binding.SessionID) &&
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

func (presenter *Presenter) PresentBackgroundCompletion(logicalSessionID string) (PresentedNotification, error) {
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
		Text:                 "Фоновая сессия завершена.",
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
	action, target, err := decodeFields(fields)
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
func presentButton(button telegramui.Button) (string, callbacktoken.Action, int, error) {
	if button.Action != telegramui.ActionPageLatest && button.Indicator != nil {
		return "", 0, 0, errors.New("only the latest-page button may contain a page indicator")
	}
	switch button.Action {
	case telegramui.ActionPagePrevious:
		if !validPageTarget(button.Target) {
			return "", 0, 0, errors.New("previous-page button requires one positive page target")
		}
		return "‹", callbacktoken.ActionPreviousPage, button.Target.Page, nil
	case telegramui.ActionPageNext:
		if !validPageTarget(button.Target) {
			return "", 0, 0, errors.New("next-page button requires one positive page target")
		}
		return "›", callbacktoken.ActionNextPage, button.Target.Page, nil
	case telegramui.ActionPageLatest:
		indicator := button.Indicator
		if indicator == nil || indicator.Current < 1 || indicator.Total < 1 ||
			indicator.Current > indicator.Total || indicator.Total > callbacktoken.MaxTarget ||
			button.Target.Page != indicator.Total || !button.Target.FollowLatest ||
			button.Target.SessionSlot != 0 || button.Target.InteractionChoice != 0 {
			return "", 0, 0, errors.New("latest-page button requires a valid indicator and follow-latest target")
		}
		return strconv.Itoa(indicator.Current) + "/" + strconv.Itoa(indicator.Total),
			callbacktoken.ActionLatestPage, 0, nil
	case telegramui.ActionStop:
		if button.Target != (telegramui.ButtonTarget{}) {
			return "", 0, 0, errors.New("stop button must not contain a target")
		}
		return "Остановить", callbacktoken.ActionStop, 0, nil
	case telegramui.ActionClose:
		if button.Target != (telegramui.ButtonTarget{}) {
			return "", 0, 0, errors.New("close button must not contain a target")
		}
		return "Закрыть", callbacktoken.ActionClose, 0, nil
	case telegramui.ActionOptions:
		if button.Target != (telegramui.ButtonTarget{}) {
			return "", 0, 0, errors.New("options button must not contain a target")
		}
		return "Опции", callbacktoken.ActionOptions, 0, nil
	case telegramui.ActionScreen:
		if button.Target != (telegramui.ButtonTarget{}) {
			return "", 0, 0, errors.New("screen button must not contain a target")
		}
		return "Screen", callbacktoken.ActionScreen, 0, nil
	case telegramui.ActionResume:
		if button.Target.Page != 0 || button.Target.FollowLatest || button.Target.SessionSlot < 0 ||
			button.Target.SessionSlot > callbacktoken.MaxTarget || button.Target.InteractionChoice != 0 {
			return "", 0, 0, errors.New("resume button target is invalid")
		}
		return "Продолжить", callbacktoken.ActionResume, 0, nil
	case telegramui.ActionMenuSessions:
		return presentGlobalButton(button, "Сессии", callbacktoken.ActionMenuSessions)
	case telegramui.ActionMenuNew:
		return presentGlobalButton(button, "Новая", callbacktoken.ActionMenuNew)
	case telegramui.ActionMenuArchive:
		return presentGlobalButton(button, "Архив", callbacktoken.ActionMenuArchive)
	case telegramui.ActionMenuStatus:
		return presentGlobalButton(button, "Статус", callbacktoken.ActionMenuStatus)
	case telegramui.ActionMenuSettings:
		return presentGlobalButton(button, "Настройки", callbacktoken.ActionMenuSettings)
	case telegramui.ActionMenuBack:
		return presentGlobalButton(button, "≡ Меню", callbacktoken.ActionMenuBack)
	case telegramui.ActionCreateCodex:
		return presentGlobalButton(button, "Codex", callbacktoken.ActionCreateCodex)
	case telegramui.ActionCreateClaude:
		return presentGlobalButton(button, "Claude", callbacktoken.ActionCreateClaude)
	case telegramui.ActionSettingsScreen:
		return presentGlobalButton(button, "Screen", callbacktoken.ActionSettingsScreen)
	case telegramui.ActionSettingsDetail:
		return presentGlobalButton(button, "Детализация", callbacktoken.ActionSettingsDetail)
	case telegramui.ActionAuthorizeCodex:
		return presentGlobalButton(button, "Авторизовать Codex", callbacktoken.ActionAuthorizeCodex)
	case telegramui.ActionAuthorizeClaude:
		return presentGlobalButton(button, "Авторизовать Claude", callbacktoken.ActionAuthorizeClaude)
	case telegramui.ActionInteractionChoice:
		if button.Target.Page != 0 || button.Target.FollowLatest || button.Target.SessionSlot != 0 ||
			button.Target.InteractionChoice < 1 || button.Target.InteractionChoice > callbacktoken.MaxTarget || button.Indicator != nil {
			return "", 0, 0, errors.New("interaction choice button target is invalid")
		}
		return "Вариант " + strconv.Itoa(button.Target.InteractionChoice), callbacktoken.ActionInteractionChoice, button.Target.InteractionChoice, nil
	case telegramui.ActionInteractionAccept:
		return presentGlobalButton(button, "Разрешить", callbacktoken.ActionInteractionAccept)
	case telegramui.ActionInteractionDecline:
		return presentGlobalButton(button, "Отклонить", callbacktoken.ActionInteractionDecline)
	case telegramui.ActionInteractionCancel:
		return presentGlobalButton(button, "Отмена", callbacktoken.ActionInteractionCancel)
	case telegramui.ActionInteractionOther:
		return presentGlobalButton(button, "Другой ответ", callbacktoken.ActionInteractionOther)
	case telegramui.ActionAcceptedTurnAssumeCompleted:
		return presentGlobalButton(button, "Считать завершённым/учтённым", callbacktoken.ActionAcceptedTurnAssumeCompleted)
	case telegramui.ActionAcceptedTurnRetryPossibleDuplicate:
		return presentGlobalButton(button, "Считать не выполненным и повторить", callbacktoken.ActionAcceptedTurnRetryPossibleDuplicate)
	case telegramui.ActionAcceptedTurnCancel:
		return presentGlobalButton(button, "Отмена", callbacktoken.ActionAcceptedTurnCancel)
	case telegramui.ActionOutboundConfirmDelivered:
		return presentGlobalButton(button, "Подтвердить доставку", callbacktoken.ActionOutboundConfirmDelivered)
	case telegramui.ActionOutboundRetryPossibleDuplicate:
		return presentGlobalButton(button, "Повторить (возможен дубль)", callbacktoken.ActionOutboundRetryPossibleDuplicate)
	case telegramui.ActionCallbackEffectConfirmed:
		return presentGlobalButton(button, "Считать выполненным", callbacktoken.ActionCallbackEffectConfirmed)
	case telegramui.ActionCallbackEffectRetryPossibleDuplicate:
		return presentGlobalButton(button, "Повторить действие (риск дубля)", callbacktoken.ActionCallbackEffectRetryPossibleDuplicate)
	case telegramui.ActionCallbackSendConfirmed:
		return presentGlobalButton(button, "Подтвердить доставку", callbacktoken.ActionCallbackSendConfirmed)
	case telegramui.ActionCallbackSendRetryPossibleDuplicate:
		return presentGlobalButton(button, "Повторить отправку (риск дубля)", callbacktoken.ActionCallbackSendRetryPossibleDuplicate)
	case telegramui.ActionStatusRecoveryAssumeDelivered:
		return presentGlobalButton(button, "Считать доставленным", callbacktoken.ActionStatusRecoveryAssumeDelivered)
	case telegramui.ActionStatusRecoveryRetryPossibleDuplicate:
		return presentGlobalButton(button, "Считать не доставленным и повторить", callbacktoken.ActionStatusRecoveryRetryPossibleDuplicate)
	case telegramui.ActionStatusRecoveryCancel:
		return presentGlobalButton(button, "Отмена", callbacktoken.ActionStatusRecoveryCancel)
	case telegramui.ActionArtifactRetry:
		return presentGlobalButton(button, "Повторить неподтверждённые", callbacktoken.ActionArtifactRetry)
	case telegramui.ActionSelectSession:
		if button.Target.Page != 0 || button.Target.FollowLatest ||
			button.Target.SessionSlot < 1 || button.Target.SessionSlot > callbacktoken.MaxTarget || button.Target.InteractionChoice != 0 {
			return "", 0, 0, errors.New("session button requires one positive session slot target")
		}
		return "Сессия " + strconv.Itoa(button.Target.SessionSlot),
			callbacktoken.ActionSelectSession, 0, nil
	default:
		return "", 0, 0, fmt.Errorf("unsupported Telegram UI action %q", button.Action)
	}
}
func presentGlobalButton(button telegramui.Button, label string, action callbacktoken.Action) (string, callbacktoken.Action, int, error) {
	if button.Target != (telegramui.ButtonTarget{}) || button.Indicator != nil {
		return "", 0, 0, errors.New("global surface button must not contain a target or indicator")
	}
	return label, action, 0, nil
}
func validPageTarget(target telegramui.ButtonTarget) bool {
	return target.Page >= 1 && target.Page <= callbacktoken.MaxTarget &&
		!target.FollowLatest && target.SessionSlot == 0 && target.InteractionChoice == 0
}
func decodeFields(fields callbacktoken.Fields) (telegramui.Action, telegramui.ButtonTarget, error) {
	switch fields.Action {
	case callbacktoken.ActionPreviousPage:
		return telegramui.ActionPagePrevious, telegramui.ButtonTarget{Page: fields.Target}, nil
	case callbacktoken.ActionNextPage:
		return telegramui.ActionPageNext, telegramui.ButtonTarget{Page: fields.Target}, nil
	case callbacktoken.ActionLatestPage:
		return telegramui.ActionPageLatest, telegramui.ButtonTarget{FollowLatest: true}, nil
	case callbacktoken.ActionSelectSession:
		return telegramui.ActionSelectSession, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionStop:
		return telegramui.ActionStop, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionClose:
		return telegramui.ActionClose, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionOptions:
		return telegramui.ActionOptions, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionScreen:
		return telegramui.ActionScreen, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionResume:
		return telegramui.ActionResume, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuSessions:
		return telegramui.ActionMenuSessions, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuNew:
		return telegramui.ActionMenuNew, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuArchive:
		return telegramui.ActionMenuArchive, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuStatus:
		return telegramui.ActionMenuStatus, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuSettings:
		return telegramui.ActionMenuSettings, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuBack:
		return telegramui.ActionMenuBack, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateCodex:
		return telegramui.ActionCreateCodex, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateClaude:
		return telegramui.ActionCreateClaude, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsScreen:
		return telegramui.ActionSettingsScreen, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsDetail:
		return telegramui.ActionSettingsDetail, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionAuthorizeCodex:
		return telegramui.ActionAuthorizeCodex, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionAuthorizeClaude:
		return telegramui.ActionAuthorizeClaude, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionInteractionChoice:
		return telegramui.ActionInteractionChoice, telegramui.ButtonTarget{InteractionChoice: fields.Target}, nil
	case callbacktoken.ActionInteractionAccept:
		return telegramui.ActionInteractionAccept, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionInteractionDecline:
		return telegramui.ActionInteractionDecline, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionInteractionCancel:
		return telegramui.ActionInteractionCancel, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionInteractionOther:
		return telegramui.ActionInteractionOther, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionAcceptedTurnAssumeCompleted:
		return telegramui.ActionAcceptedTurnAssumeCompleted, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionAcceptedTurnRetryPossibleDuplicate:
		return telegramui.ActionAcceptedTurnRetryPossibleDuplicate, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionAcceptedTurnCancel:
		return telegramui.ActionAcceptedTurnCancel, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionOutboundConfirmDelivered:
		return telegramui.ActionOutboundConfirmDelivered, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionOutboundRetryPossibleDuplicate:
		return telegramui.ActionOutboundRetryPossibleDuplicate, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCallbackEffectConfirmed:
		return telegramui.ActionCallbackEffectConfirmed, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCallbackEffectRetryPossibleDuplicate:
		return telegramui.ActionCallbackEffectRetryPossibleDuplicate, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCallbackSendConfirmed:
		return telegramui.ActionCallbackSendConfirmed, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCallbackSendRetryPossibleDuplicate:
		return telegramui.ActionCallbackSendRetryPossibleDuplicate, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionStatusRecoveryAssumeDelivered:
		return telegramui.ActionStatusRecoveryAssumeDelivered, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionStatusRecoveryRetryPossibleDuplicate:
		return telegramui.ActionStatusRecoveryRetryPossibleDuplicate, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionStatusRecoveryCancel:
		return telegramui.ActionStatusRecoveryCancel, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionArtifactRetry:
		return telegramui.ActionArtifactRetry, telegramui.ButtonTarget{}, nil
	default:
		return "", telegramui.ButtonTarget{}, errors.New("authenticated callback contains an unsupported action")
	}
}
func isCanonicalLogicalSessionUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
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
