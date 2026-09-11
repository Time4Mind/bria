package telegramtrace

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"bria/internal/callbackdiagnostic"
	"bria/internal/safelog"
)

// Record encodes an in-memory trace for persistence without raw identities.
func Record(event Event, key []byte, sequence uint64) safelog.Event {
	ref := func(domain, identity string) string {
		if identity == "" {
			return ""
		}
		digest := hmac.New(sha256.New, key)
		_, _ = digest.Write([]byte("bria-telegramtrace-v2\x00" + domain + "\x00" + identity))
		return "c_" + hex.EncodeToString(digest.Sum(nil))
	}
	fields := map[string]string{
		"stage": event.Stage, "duration_ms": strconv.FormatInt(event.Duration.Milliseconds(), 10),
		"sequence": strconv.FormatUint(sequence, 10), "run_ref": ref("run", "process"),
	}
	if input := inputOperation(event.OperationID); input != "" {
		fields["input_ref"] = ref("input", input)
	}
	if event.hasControllerEvent {
		controllerFields(fields, event.controllerEvent, ref)
	}
	for field, value := range map[string]string{
		"entity_kind": string(event.UpdateKind), "action": string(event.Action), "operation": string(event.Effect),
		"callback_ref": ref("callback", event.CallbackID), "session_ref": ref("session", event.SessionID),
		"presentation_ref": ref("presentation", event.PresentationID),
	} {
		if value != "" {
			fields[field] = value
		}
	}
	for _, card := range []struct {
		field         string
		chat, carrier int64
	}{
		{"card_ref", event.ChatID, event.CarrierID}, {"expected_card_ref", event.ExpectedChatID, event.ExpectedCarrierID},
	} {
		if card.chat != 0 || card.carrier != 0 {
			fields[card.field] = ref("card", strconv.FormatInt(card.chat, 10)+":"+strconv.FormatInt(card.carrier, 10))
		}
	}
	buttons := make([]string, 0, min(len(event.ButtonIDs), 32))
	for _, id := range event.ButtonIDs[:min(len(event.ButtonIDs), 32)] {
		if id != "" {
			buttons = append(buttons, ref("callback", id))
		}
	}
	if len(buttons) > 0 {
		fields["button_refs"] = strings.Join(buttons, ",")
	}
	if len(event.ButtonIDs) > 32 {
		fields["button_refs_truncated"] = strconv.Itoa(len(event.ButtonIDs) - 32)
	}
	if event.HasPage {
		fields["page"], fields["pages"] = strconv.Itoa(event.Page), strconv.Itoa(event.Pages)
		fields["follow_latest"] = strconv.FormatBool(event.FollowLatest)
	}
	if (event.Stage == "callback.accept" || event.Stage == "callback.discard") && event.Reason != "" && event.PresentationID != "" {
		fields["retired"] = strconv.FormatBool(event.Retired)
	}
	if event.Action != "" && event.Target >= 0 {
		fields["target"] = strconv.Itoa(event.Target)
	}
	category, redacted := "", ""
	if event.Error != "" || event.Reason != "" {
		category = callbackdiagnostic.SafeCode(event.Reason)
		fields["reason"] = category
	}
	if event.Error != "" {
		redacted = "[REDACTED]"
	}
	return safelog.Event{
		Class: safelog.Detailed, Type: "telegram.flow_stage", EntityID: ref("operation", event.OperationID),
		Time: event.Time, Result: event.Result, ErrorCategory: category, Error: redacted, Fields: fields,
	}
}

func inputOperation(operation string) string {
	var value string
	switch {
	case strings.HasPrefix(operation, "status:"):
		value = strings.TrimPrefix(operation, "status:")
	case strings.HasPrefix(operation, "telegram-update:"):
		value = strings.TrimPrefix(operation, "telegram-update:")
		if end := strings.IndexByte(value, ':'); end >= 0 {
			value = value[:end]
		}
	default:
		return ""
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return ""
	}
	return "telegram-update:" + value
}
