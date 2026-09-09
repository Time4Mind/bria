package controllerhistory

import (
	"bria/internal/cardtranscript"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontrolport"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
)

type runtimeEventHistory interface {
	InsertCardRuntimeEvent(context.Context, domain.SessionID, string, string, string, string) error
}

func Text(event sessionruntime.TurnEvent) string {
	if event.Kind != sessionruntime.EventTool || event.Metadata == nil {
		return event.Text
	}
	m := event.Metadata
	return cardtranscript.EncodeTool(cardtranscript.Tool{ID: m.ItemID, Name: m.Name, Arguments: m.Arguments, Output: m.Result, Status: m.Status})
}

func Operation(messageID string, index int, eventID string) string {
	if eventID != "" {
		return fmt.Sprintf("%s:event:%x", messageID, sha256.Sum256([]byte(eventID)))
	}
	return messageID + ":event:" + strconv.Itoa(index)
}

func (h *History) PersistEvent(ctx context.Context, id domain.SessionID, message string, event sessionruntime.TurnEvent) error {
	if event.ID == "" {
		h.Append(ctx, id, message, event)
		return nil
	}
	store, ok := h.Store.(runtimeEventHistory)
	if !ok {
		return errors.New("identified runtime history persistence is unavailable")
	}
	if err := store.InsertCardRuntimeEvent(ctx, id, message, event.ID, Text(event), string(event.Kind)); err != nil {
		return err
	}
	if historyStore, ok := h.Store.(telegramcontrolport.CardHistoryStore); ok {
		history, err := historyStore.LoadCardHistory(ctx, id)
		if err != nil {
			return err
		}
		h.Mu.Lock()
		(*h.Values)[id] = history
		h.Mu.Unlock()
	}
	return nil
}
