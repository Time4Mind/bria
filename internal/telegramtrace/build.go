package telegramtrace

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"bria/internal/callbackdiagnostic"
	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/telegrambridge"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramtransport"
	"bria/internal/telegramui"
)

func Completed(stage, operationID string, update coordinator.Update, action telegramui.Action, effect telegrampipeline.CallbackEffect, started time.Time, err error) Event {
	return Event{Stage: stage, OperationID: operationID, UpdateID: update.ID, UpdateKind: update.Kind, Action: action, Effect: effect, Result: Result(err), Error: Error(err), Reason: Reason(err), Time: time.Now().UTC(), Duration: time.Since(started)}
}

func Result(err error) string {
	if err != nil {
		return "failed"
	}
	return "completed"
}

func Error(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func Transport(stage, operationID string, plan telegrampipeline.CallbackPlan, started time.Time, err error) Event {
	return Completed(stage, operationID, coordinator.Update{ID: plan.UpdateID, Kind: coordinator.UpdateCallback}, plan.Action, plan.Effect, started, err)
}

func Reason(err error) string {
	if err == nil {
		return ""
	}
	if code := callbackdiagnostic.Inspect(err).Code; code != "" {
		return callbackdiagnostic.SafeCode(code)
	}
	if class, ok := telegramtransport.ClassOf(err); ok {
		return callbackdiagnostic.SafeCode("transport_" + string(class))
	}
	for _, item := range []struct {
		err  error
		code string
	}{
		{callbacktoken.ErrInvalidToken, "token_invalid"}, {callbacktoken.ErrExpired, "token_expired"},
		{callbacktoken.ErrInvalidFields, "token_fields_invalid"}, {callbacktoken.ErrUnsupportedVersion, "token_version_unsupported"},
		{callbacktoken.ErrUnknownAction, "token_action_unknown"}, {telegrampipeline.ErrStaleCallback, "presentation_missing"},
		{telegrampipeline.ErrReplayedCallback, "presentation_replayed"}, {context.Canceled, "cancelled"}, {context.DeadlineExceeded, "deadline_exceeded"},
	} {
		if errors.Is(err, item.err) {
			return item.code
		}
	}
	return "operation_failed"
}

func Timestamp(event Event) Event {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	return event
}

func Callback(operationID string, update coordinator.Update, accepted telegrampipeline.AcceptedCallback, started time.Time, err error) Event {
	event := Completed("callback.accept", operationID, update, accepted.Action, "", started, err)
	// This digest matches Presenter.CallbackTokenID; no signed token enters a trace.
	digest := sha256.Sum256([]byte(update.Text))
	event.CallbackID = base64.RawURLEncoding.EncodeToString(digest[:16])
	event.ChatID, event.CarrierID = update.ConversationID, update.SourceMessageID
	event.SessionID = string(accepted.SessionID)
	event.Target = max(accepted.Target.Page, accepted.Target.Choice, accepted.Target.InteractionChoice)
	if err != nil {
		details := callbackdiagnostic.Inspect(err)
		event.Action, event.SessionID = telegramui.Action(details.Action), details.SessionID
		event.ExpectedChatID, event.ExpectedCarrierID = details.CardChatID, details.CardMessageID
		event.PresentationID, event.Retired = details.PresentationID, details.Retired
		event.Target = details.Target
	}
	return event
}

func Card(stage, operationID, sessionID string, chatID, carrierID int64, presentation telegrambridge.KeyboardPresentation, view telegramui.PageView, hasPage bool, started time.Time, err error) Event {
	event := Completed(stage, operationID, coordinator.Update{}, "", "", started, err)
	event.ChatID, event.CarrierID, event.SessionID = chatID, carrierID, sessionID
	event.PresentationID = presentation.SessionID
	event.ButtonIDs = append([]string(nil), presentation.TokenIDs...)
	event.Page, event.Pages, event.HasPage, event.FollowLatest = view.Page, view.Pages, hasPage, view.FollowLatest
	return event
}
