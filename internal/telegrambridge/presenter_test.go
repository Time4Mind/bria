package telegrambridge_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"bria/internal/callbacktoken"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramui"
)

const testLogicalSessionID = "00112233-4455-6677-8899-aabbccddeeff"

var testSelectableSessionIDs = []string{
	"11112233-4455-6677-8899-aabbccddeeff",
	"22222233-4455-6677-8899-aabbccddeeff",
	"33332233-4455-6677-8899-aabbccddeeff",
}

func TestPresenterPreservesCanonicalRowsLabelsAndSignedSemanticCallbacks(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	codec := mustCallbackCodec(t, func() time.Time { return now })
	presenter := mustPresenter(t, codec, func() time.Time { return now }, 15*time.Minute)
	keyboard, err := telegramui.ProjectCardKeyboard(telegramui.CardKeyboardInput{
		View:            telegramui.PageView{Page: 1, Pages: 3},
		Working:         true,
		OptionsExpanded: true,
		SessionRowSizes: []int{2, 1},
	})
	if err != nil {
		t.Fatalf("ProjectCardKeyboard() error = %v", err)
	}

	selectableBefore := append([]string(nil), testSelectableSessionIDs...)
	markup, err := presenter.PresentKeyboard(testLogicalSessionID, testSelectableSessionIDs, keyboard)
	if err != nil {
		t.Fatalf("PresentKeyboard() error = %v", err)
	}
	wantLabels := [][]string{
		{"‹", "1/3", "›"},
		{"Остановить", "Опции"},
		{"Screen"},
		{"Сессия 1", "Сессия 2"},
		{"Сессия 3"},
	}
	if got := labels(markup.InlineKeyboard); !reflect.DeepEqual(got, wantLabels) {
		t.Fatalf("labels/rows = %#v, want %#v", got, wantLabels)
	}
	if !reflect.DeepEqual(testSelectableSessionIDs, selectableBefore) {
		t.Fatalf("PresentKeyboard() mutated selectable sessions: got %#v, want %#v", testSelectableSessionIDs, selectableBefore)
	}

	wantCallbacks := [][]telegrambridge.Callback{
		{
			{SessionID: testLogicalSessionID, Action: telegramui.ActionPagePrevious, Target: telegramui.ButtonTarget{Page: 3}},
			{SessionID: testLogicalSessionID, Action: telegramui.ActionPageLatest, Target: telegramui.ButtonTarget{FollowLatest: true}},
			{SessionID: testLogicalSessionID, Action: telegramui.ActionPageNext, Target: telegramui.ButtonTarget{Page: 2}},
		},
		{
			{SessionID: testLogicalSessionID, Action: telegramui.ActionStop},
			{SessionID: testLogicalSessionID, Action: telegramui.ActionOptions},
		},
		{{SessionID: testLogicalSessionID, Action: telegramui.ActionScreen}},
		{
			{SessionID: testSelectableSessionIDs[0], Action: telegramui.ActionSelectSession},
			{SessionID: testSelectableSessionIDs[1], Action: telegramui.ActionSelectSession},
		},
		{{SessionID: testSelectableSessionIDs[2], Action: telegramui.ActionSelectSession}},
	}
	for rowIndex, row := range markup.InlineKeyboard {
		for buttonIndex, button := range row {
			if len(button.CallbackData) != callbacktoken.EncodedLength {
				t.Errorf("callback_data %d:%d length = %d, want %d", rowIndex, buttonIndex, len(button.CallbackData), callbacktoken.EncodedLength)
			}
			if strings.Contains(button.CallbackData, testLogicalSessionID) {
				t.Errorf("callback_data %d:%d exposes logical session UUID", rowIndex, buttonIndex)
			}
			got, err := presenter.DecodeCallback(button.CallbackData)
			if err != nil {
				t.Fatalf("DecodeCallback(%d:%d) error = %v", rowIndex, buttonIndex, err)
			}
			if want := wantCallbacks[rowIndex][buttonIndex]; got != want {
				t.Errorf("DecodeCallback(%d:%d) = %#v, want %#v", rowIndex, buttonIndex, got, want)
			}
			fields, err := codec.Decode(button.CallbackData)
			if err != nil {
				t.Fatalf("codec.Decode(%d:%d) error = %v", rowIndex, buttonIndex, err)
			}
			if fields.ExpiresAt != now.Add(15*time.Minute) {
				t.Errorf("expiry %d:%d = %s, want %s", rowIndex, buttonIndex, fields.ExpiresAt, now.Add(15*time.Minute))
			}
		}
	}
}

func TestPresenterUsesCloseLabelForInactiveCard(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	keyboard, err := telegramui.ProjectCardKeyboard(telegramui.CardKeyboardInput{
		View: telegramui.PageView{Page: 1, Pages: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	markup, err := presenter.PresentKeyboard(testLogicalSessionID, nil, keyboard)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := markup.InlineKeyboard[1][0].Text, "Закрыть"; got != want {
		t.Fatalf("close label = %q, want %q", got, want)
	}
	callback, err := presenter.DecodeCallback(markup.InlineKeyboard[1][0].CallbackData)
	if err != nil || callback.Action != telegramui.ActionClose {
		t.Fatalf("close callback = %#v, error %v", callback, err)
	}
}

func TestPresenterSignsResumeForOwningArchivedSession(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	markup, err := presenter.PresentKeyboard(testLogicalSessionID, nil, oneButton(telegramui.Button{Action: telegramui.ActionResume}))
	if err != nil {
		t.Fatal(err)
	}
	if markup.InlineKeyboard[0][0].Text != "Продолжить" {
		t.Fatalf("resume label = %q", markup.InlineKeyboard[0][0].Text)
	}
	callback, err := presenter.DecodeCallback(markup.InlineKeyboard[0][0].CallbackData)
	if err != nil || callback.Action != telegramui.ActionResume || callback.SessionID != testLogicalSessionID {
		t.Fatalf("resume callback = %#v, %v", callback, err)
	}
}

func TestPresenterSignsEveryGlobalSurfaceActionWithoutRawCallbackData(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	actions := []telegramui.Action{
		telegramui.ActionMenuSessions, telegramui.ActionMenuNew, telegramui.ActionMenuArchive,
		telegramui.ActionMenuStatus, telegramui.ActionMenuSettings, telegramui.ActionMenuBack,
		telegramui.ActionCreateCodex, telegramui.ActionCreateClaude,
		telegramui.ActionSettingsScreen, telegramui.ActionSettingsDetail,
		telegramui.ActionAuthorizeCodex, telegramui.ActionAuthorizeClaude,
	}
	rows := make([]telegramui.ButtonRow, len(actions))
	for index, action := range actions {
		rows[index] = telegramui.ButtonRow{{Action: action}}
	}
	presentation, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, telegramui.CardKeyboard{Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range actions {
		button := presentation.Markup.InlineKeyboard[index][0]
		if strings.Contains(button.CallbackData, "menu:") || strings.Contains(button.CallbackData, "settings:") || len(button.CallbackData) != callbacktoken.EncodedLength {
			t.Fatalf("action %q callback_data=%q is not an opaque signed token", want, button.CallbackData)
		}
		decoded, err := presenter.DecodeCallback(button.CallbackData)
		if err != nil || decoded.Action != want || decoded.SessionID != telegramui.GlobalSurfaceID {
			t.Fatalf("action %q decoded=%#v err=%v", want, decoded, err)
		}
	}
}

func TestPresenterBindsGlobalSessionListAndArchiveButtonsToExactTargets(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	targets := []string{testSelectableSessionIDs[0], testSelectableSessionIDs[1]}
	presentation, err := presenter.PresentKeyboardWithManifest(
		telegramui.GlobalSurfaceID,
		targets,
		telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
			{{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 1}}},
			{{Action: telegramui.ActionResume, Target: telegramui.ButtonTarget{SessionSlot: 2}}},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []telegrambridge.Callback{
		{SessionID: targets[0], Action: telegramui.ActionSelectSession},
		{SessionID: targets[1], Action: telegramui.ActionResume},
	} {
		got, err := presenter.DecodeCallback(presentation.Markup.InlineKeyboard[index][0].CallbackData)
		if err != nil || got != want {
			t.Fatalf("target button %d = %#v, %v want %#v", index, got, err, want)
		}
	}
}

func TestPresenterBindsInteractionActionsToOpaqueRequestAndExactSession(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{{Action: telegramui.ActionInteractionChoice, Target: telegramui.ButtonTarget{InteractionChoice: 2}}},
		{{Action: telegramui.ActionInteractionAccept}, {Action: telegramui.ActionInteractionDecline}, {Action: telegramui.ActionInteractionCancel}, {Action: telegramui.ActionInteractionOther}},
	}}
	if _, err := presenter.PresentKeyboardWithManifest(testLogicalSessionID, nil, keyboard); err == nil {
		t.Fatal("unbound interaction keyboard was accepted")
	}
	presentation, err := presenter.PresentInteractionKeyboardWithManifest(testLogicalSessionID, "provider-request-opaque", keyboard)
	if err != nil {
		t.Fatal(err)
	}
	if presentation.InteractionRequestID != "provider-request-opaque" {
		t.Fatalf("request binding = %q", presentation.InteractionRequestID)
	}
	wants := []telegrambridge.Callback{
		{SessionID: testLogicalSessionID, Action: telegramui.ActionInteractionChoice, Target: telegramui.ButtonTarget{InteractionChoice: 2}},
		{SessionID: testLogicalSessionID, Action: telegramui.ActionInteractionAccept},
		{SessionID: testLogicalSessionID, Action: telegramui.ActionInteractionDecline},
		{SessionID: testLogicalSessionID, Action: telegramui.ActionInteractionCancel},
		{SessionID: testLogicalSessionID, Action: telegramui.ActionInteractionOther},
	}
	index := 0
	for _, row := range presentation.Markup.InlineKeyboard {
		for _, button := range row {
			got, err := presenter.DecodeCallback(button.CallbackData)
			if err != nil || got != wants[index] {
				t.Fatalf("interaction button %d = %#v, %v want %#v", index, got, err, wants[index])
			}
			index++
		}
	}
}

func TestPresenterBindsOutboundResolutionToExactServerSideOperation(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionOutboundConfirmDelivered},
		{Action: telegramui.ActionOutboundRetryPossibleDuplicate},
	}}}
	if _, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, keyboard); err == nil {
		t.Fatal("unbound outbound resolution keyboard was accepted")
	}
	presentation, err := presenter.PresentOutboundResolutionKeyboardWithManifest("status:original-42", 42, keyboard)
	if err != nil {
		t.Fatal(err)
	}
	if presentation.OutboundOperationID != "status:original-42" || presentation.OutboundUpdateID != 42 {
		t.Fatalf("outbound manifest = %#v", presentation)
	}
	for index, want := range []telegramui.Action{telegramui.ActionOutboundConfirmDelivered, telegramui.ActionOutboundRetryPossibleDuplicate} {
		got, err := presenter.DecodeCallback(presentation.Markup.InlineKeyboard[0][index].CallbackData)
		if err != nil || got.SessionID != telegramui.GlobalSurfaceID || got.Action != want {
			t.Fatalf("outbound button %d = %#v, %v", index, got, err)
		}
	}
}

func TestPresenterBindsCallbackRecoveryToExactUnknownOperation(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	binding := telegrambridge.CallbackRecoveryBinding{
		OperationID: "status:unknown-42", UpdateID: 42, SessionID: testLogicalSessionID,
		ChatID: 7, MessageID: 99, Phase: "effect_unknown",
	}
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionCallbackEffectConfirmed},
		{Action: telegramui.ActionCallbackEffectRetryPossibleDuplicate},
	}}}
	if _, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, keyboard); err == nil {
		t.Fatal("unbound callback recovery keyboard was accepted")
	}
	presentation, err := presenter.PresentCallbackRecoveryKeyboardWithManifest(binding, keyboard)
	if err != nil {
		t.Fatal(err)
	}
	if presentation.Recovery == nil || *presentation.Recovery != binding {
		t.Fatalf("callback recovery manifest = %#v", presentation.Recovery)
	}
}

func TestPresenterDecodesAcceptedTurnRecoveryActions(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec := mustCallbackCodec(t, func() time.Time { return now })
	presenter := mustPresenter(t, codec, func() time.Time { return now }, time.Minute)

	for action, want := range map[callbacktoken.Action]telegramui.Action{
		callbacktoken.ActionAcceptedTurnAssumeCompleted:        telegramui.ActionAcceptedTurnAssumeCompleted,
		callbacktoken.ActionAcceptedTurnRetryPossibleDuplicate: telegramui.ActionAcceptedTurnRetryPossibleDuplicate,
		callbacktoken.ActionAcceptedTurnCancel:                 telegramui.ActionAcceptedTurnCancel,
	} {
		token, err := codec.Encode(callbacktoken.Fields{Action: action, SessionID: testLogicalSessionID, ExpiresAt: now.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := presenter.DecodeCallback(token)
		if err != nil || decoded.Action != want || decoded.SessionID != testLogicalSessionID {
			t.Fatalf("decoded accepted-turn recovery = %#v, %v; want %q", decoded, err, want)
		}
	}
}

func TestPresenterDecodesDistinctStatusRecoveryActions(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec := mustCallbackCodec(t, func() time.Time { return now })
	presenter := mustPresenter(t, codec, func() time.Time { return now }, time.Minute)

	for action, want := range map[callbacktoken.Action]telegramui.Action{
		callbacktoken.ActionStatusRecoveryAssumeDelivered:        telegramui.ActionStatusRecoveryAssumeDelivered,
		callbacktoken.ActionStatusRecoveryRetryPossibleDuplicate: telegramui.ActionStatusRecoveryRetryPossibleDuplicate,
		callbacktoken.ActionStatusRecoveryCancel:                 telegramui.ActionStatusRecoveryCancel,
	} {
		token, err := codec.Encode(callbacktoken.Fields{Action: action, SessionID: telegramui.GlobalSurfaceID, ExpiresAt: now.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := presenter.DecodeCallback(token)
		if err != nil || decoded.Action != want || decoded.SessionID != telegramui.GlobalSurfaceID {
			t.Fatalf("decoded status recovery = %#v, %v; want %q", decoded, err, want)
		}
	}
}

func TestPresenterBindsStatusRecoveryToExactUnknownStatus(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	binding := telegrambridge.StatusRecoveryBinding{
		OperationID: "status:731", UpdateID: 731,
		Scope: statusrecovery.Scope{Kind: statusrecovery.ScopeSession, SessionID: testLogicalSessionID}, Sequence: 731,
	}
	binding.Carrier.ChatID, binding.Carrier.MessageID = 42, 99
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{{Action: telegramui.ActionStatusRecoveryAssumeDelivered}},
		{{Action: telegramui.ActionStatusRecoveryRetryPossibleDuplicate}},
		{{Action: telegramui.ActionStatusRecoveryCancel}},
	}}
	if _, err := presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, nil, keyboard); err == nil {
		t.Fatal("unbound status recovery keyboard was accepted")
	}
	presentation, err := presenter.PresentStatusRecoveryKeyboardWithManifest(binding, keyboard)
	if err != nil {
		t.Fatal(err)
	}
	if presentation.StatusRecovery == nil || *presentation.StatusRecovery != binding || presentation.SessionID != telegramui.GlobalSurfaceID {
		t.Fatalf("status recovery manifest = %#v", presentation)
	}
	wantLabels := []string{"Считать доставленным", "Считать не доставленным и повторить", "Отмена"}
	for index, row := range presentation.Markup.InlineKeyboard {
		if len(row) != 1 || row[0].Text != wantLabels[index] {
			t.Fatalf("row %d = %#v, want label %q", index, row, wantLabels[index])
		}
		decoded, decodeErr := presenter.DecodeCallback(row[0].CallbackData)
		if decodeErr != nil || decoded.SessionID != telegramui.GlobalSurfaceID {
			t.Fatalf("row %d callback = %#v, %v", index, decoded, decodeErr)
		}
	}
}

func TestPresenterBindsAcceptedTurnRecoveryToExactServerSideTurn(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	binding := telegrambridge.AcceptedTurnRecoveryBinding{
		SessionID: testLogicalSessionID, MessageID: "telegram-update:301", BindingGeneration: 7,
	}
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{{Action: telegramui.ActionAcceptedTurnAssumeCompleted}},
		{{Action: telegramui.ActionAcceptedTurnRetryPossibleDuplicate}},
		{{Action: telegramui.ActionAcceptedTurnCancel}},
	}}
	if _, err := presenter.PresentKeyboardWithManifest(testLogicalSessionID, nil, keyboard); err == nil {
		t.Fatal("unbound accepted-turn recovery keyboard was accepted")
	}
	presentation, err := presenter.PresentAcceptedTurnRecoveryKeyboardWithManifest(binding, keyboard)
	if err != nil {
		t.Fatal(err)
	}
	if presentation.AcceptedTurnRecovery == nil || *presentation.AcceptedTurnRecovery != binding || presentation.SessionID != string(binding.SessionID) {
		t.Fatalf("accepted-turn recovery manifest = %#v", presentation)
	}
	wantLabels := []string{"Считать завершённым/учтённым", "Считать не выполненным и повторить", "Отмена"}
	for index, row := range presentation.Markup.InlineKeyboard {
		if len(row) != 1 || row[0].Text != wantLabels[index] {
			t.Fatalf("row %d = %#v, want label %q", index, row, wantLabels[index])
		}
		decoded, err := presenter.DecodeCallback(row[0].CallbackData)
		if err != nil || decoded.SessionID != string(binding.SessionID) {
			t.Fatalf("row %d callback = %#v, %v", index, decoded, err)
		}
	}
	for name, invalid := range map[string]telegrambridge.AcceptedTurnRecoveryBinding{
		"missing message":    {SessionID: testLogicalSessionID, BindingGeneration: 7},
		"missing generation": {SessionID: testLogicalSessionID, MessageID: "telegram-update:301"},
		"invalid session":    {SessionID: "active", MessageID: "telegram-update:301", BindingGeneration: 7},
		"global surface":     {SessionID: telegramui.GlobalSurfaceID, MessageID: "telegram-update:301", BindingGeneration: 7},
	} {
		if _, err := presenter.PresentAcceptedTurnRecoveryKeyboardWithManifest(invalid, keyboard); err == nil {
			t.Fatalf("%s binding was accepted", name)
		}
	}
}

func TestPresenterRejectsTamperedAndExpiredCallbackBeforeReturningSemantics(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	clock := func() time.Time { return now }
	presenter := mustPresenter(t, mustCallbackCodec(t, clock), clock, time.Minute)
	markup, err := presenter.PresentKeyboard(testLogicalSessionID, nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
		{Action: telegramui.ActionScreen},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	token := markup.InlineKeyboard[0][0].CallbackData
	tampered := []byte(token)
	if tampered[10] == 'A' {
		tampered[10] = 'B'
	} else {
		tampered[10] = 'A'
	}
	if got, err := presenter.DecodeCallback(string(tampered)); err == nil || got != (telegrambridge.Callback{}) {
		t.Fatalf("tampered DecodeCallback() = %#v, %v; want zero callback and error", got, err)
	}

	now = now.Add(2 * time.Minute)
	if got, err := presenter.DecodeCallback(token); !errors.Is(err, callbacktoken.ErrExpired) || got != (telegrambridge.Callback{}) {
		t.Fatalf("expired DecodeCallback() = %#v, %v; want zero callback and ErrExpired", got, err)
	}
}

func TestPresenterRejectsInvalidKeyboardAndTelegramButtonBounds(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	valid := telegramui.Button{Action: telegramui.ActionScreen}
	tooWide := make(telegramui.ButtonRow, 9)
	for index := range tooWide {
		tooWide[index] = valid
	}
	tooMany := make([]telegramui.ButtonRow, 13)
	for rowIndex := range tooMany {
		tooMany[rowIndex] = make(telegramui.ButtonRow, 8)
		for buttonIndex := range tooMany[rowIndex] {
			tooMany[rowIndex][buttonIndex] = valid
		}
	}

	tests := []struct {
		name     string
		session  string
		keyboard telegramui.CardKeyboard
	}{
		{name: "noncanonical session UUID", session: "00112233445566778899aabbccddeeff", keyboard: oneButton(valid)},
		{name: "no rows", session: testLogicalSessionID, keyboard: telegramui.CardKeyboard{}},
		{name: "empty row", session: testLogicalSessionID, keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{nil}}},
		{name: "more than eight in a row", session: testLogicalSessionID, keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{tooWide}}},
		{name: "more than one hundred buttons", session: testLogicalSessionID, keyboard: telegramui.CardKeyboard{Rows: tooMany}},
		{name: "unknown action", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: "future"})},
		{name: "previous without page", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: telegramui.ActionPagePrevious})},
		{name: "next with session slot", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: telegramui.ActionPageNext, Target: telegramui.ButtonTarget{Page: 1, SessionSlot: 1}})},
		{name: "latest without indicator", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: telegramui.ActionPageLatest, Target: telegramui.ButtonTarget{Page: 2, FollowLatest: true}})},
		{name: "latest target differs from total", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: telegramui.ActionPageLatest, Target: telegramui.ButtonTarget{Page: 2, FollowLatest: true}, Indicator: &telegramui.PageIndicator{Current: 1, Total: 3}})},
		{name: "invalid indicator", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: telegramui.ActionPageLatest, Target: telegramui.ButtonTarget{Page: 2, FollowLatest: true}, Indicator: &telegramui.PageIndicator{Current: 3, Total: 2}})},
		{name: "lifecycle with target", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: telegramui.ActionStop, Target: telegramui.ButtonTarget{Page: 1}})},
		{name: "select without slot", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: telegramui.ActionSelectSession})},
		{name: "indicator on non-latest", session: testLogicalSessionID, keyboard: oneButton(telegramui.Button{Action: telegramui.ActionScreen, Indicator: &telegramui.PageIndicator{Current: 1, Total: 1}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selectable := []string(nil)
			if test.name == "select without slot" {
				selectable = []string{testSelectableSessionIDs[0]}
			}
			if _, err := presenter.PresentKeyboard(test.session, selectable, test.keyboard); err == nil {
				t.Fatal("PresentKeyboard() error = nil")
			}
		})
	}
}

func TestPresenterRequiresExactOrderedSelectableSessionMapping(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	twoSlots := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 1}}},
		{{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 2}}},
	}}
	tests := []struct {
		name       string
		selectable []string
		keyboard   telegramui.CardKeyboard
	}{
		{name: "missing selectable session", selectable: testSelectableSessionIDs[:1], keyboard: twoSlots},
		{name: "extra selectable session", selectable: testSelectableSessionIDs, keyboard: twoSlots},
		{name: "noncanonical selected UUID", selectable: []string{testSelectableSessionIDs[0], "provider-session-id"}, keyboard: twoSlots},
		{name: "slot order starts at two", selectable: testSelectableSessionIDs[:1], keyboard: oneButton(telegramui.Button{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 2}})},
		{name: "duplicate slot", selectable: testSelectableSessionIDs[:2], keyboard: telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{
			{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 1}},
			{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 1}},
		}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := presenter.PresentKeyboard(testLogicalSessionID, test.selectable, test.keyboard); err == nil {
				t.Fatal("PresentKeyboard() error = nil")
			}
		})
	}
}

func TestNewPresenterRejectsMissingCodecClockAndInvalidTTL(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	codec := mustCallbackCodec(t, func() time.Time { return now })
	if _, err := telegrambridge.NewPresenter(nil, func() time.Time { return now }, time.Minute); err == nil {
		t.Fatal("NewPresenter(nil codec) error = nil")
	}
	if _, err := telegrambridge.NewPresenter(codec, nil, time.Minute); err == nil {
		t.Fatal("NewPresenter(nil clock) error = nil")
	}
	for _, ttl := range []time.Duration{0, -1, time.Second - 1} {
		if _, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, ttl); err == nil {
			t.Errorf("NewPresenter(ttl=%s) error = nil", ttl)
		}
	}
}

func TestPresenterReturnsManifestForDurableCarrierBinding(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	presentation, err := presenter.PresentKeyboardWithManifest(
		testLogicalSessionID,
		nil,
		oneButton(telegramui.Button{Action: telegramui.ActionScreen}),
	)
	if err != nil {
		t.Fatalf("PresentKeyboardWithManifest() error = %v", err)
	}
	if presentation.SessionID != testLogicalSessionID || presentation.ExpiresAt != now.Add(time.Minute) {
		t.Fatalf("presentation identity = %#v", presentation)
	}
	if len(presentation.TokenIDs) != 1 || presentation.TokenIDs[0] == "" {
		t.Fatalf("presentation token IDs = %#v, want one opaque ID", presentation.TokenIDs)
	}
	button := presentation.Markup.InlineKeyboard[0][0]
	decoded, err := presenter.DecodeCallbackWithMetadata(button.CallbackData)
	if err != nil {
		t.Fatalf("DecodeCallbackWithMetadata() error = %v", err)
	}
	if decoded.Callback.Action != telegramui.ActionScreen || decoded.Callback.SessionID != testLogicalSessionID {
		t.Fatalf("decoded callback = %#v", decoded.Callback)
	}
	if decoded.TokenID != presentation.TokenIDs[0] || decoded.ExpiresAt != presentation.ExpiresAt {
		t.Fatalf("decoded metadata = %#v, presentation = %#v", decoded, presentation)
	}
}

func TestPresenterBuildsOneCompactBackgroundCompletionWithoutFinal(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	notification, err := presenter.PresentBackgroundCompletion(testLogicalSessionID)
	if err != nil {
		t.Fatalf("PresentBackgroundCompletion() error = %v", err)
	}
	if got, want := notification.Text, "Фоновая сессия завершена."; got != want {
		t.Fatalf("notification text = %q, want %q", got, want)
	}
	if strings.Contains(notification.Text, "SECRET FINAL") {
		t.Fatal("compact notification unexpectedly contains final content")
	}
	if len(notification.Markup.InlineKeyboard) != 1 || len(notification.Markup.InlineKeyboard[0]) != 1 ||
		notification.Markup.InlineKeyboard[0][0].Text != "Открыть" {
		t.Fatalf("notification keyboard = %#v, want one open button", notification.Markup)
	}
	decoded, err := presenter.DecodeCallback(notification.Markup.InlineKeyboard[0][0].CallbackData)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Action != telegramui.ActionSelectSession || decoded.SessionID != testLogicalSessionID {
		t.Fatalf("notification callback = %#v", decoded)
	}
	if len(notification.TokenIDs) != 1 || notification.TokenIDs[0] == "" {
		t.Fatalf("notification manifest = %#v", notification.TokenIDs)
	}
}

func mustCallbackCodec(t *testing.T, now func() time.Time) *callbacktoken.Codec {
	t.Helper()
	codec, err := callbacktoken.New(
		bytes.Repeat([]byte{0x42}, 32),
		bytes.NewReader(bytes.Repeat([]byte{0x24}, 4096)),
		now,
	)
	if err != nil {
		t.Fatalf("callbacktoken.New() error = %v", err)
	}
	return codec
}

func mustPresenter(t *testing.T, codec *callbacktoken.Codec, now func() time.Time, ttl time.Duration) *telegrambridge.Presenter {
	t.Helper()
	presenter, err := telegrambridge.NewPresenter(codec, now, ttl)
	if err != nil {
		t.Fatalf("NewPresenter() error = %v", err)
	}
	return presenter
}

func labels(rows [][]telegram.InlineKeyboardButton) [][]string {
	result := make([][]string, len(rows))
	for rowIndex, row := range rows {
		result[rowIndex] = make([]string, len(row))
		for buttonIndex, button := range row {
			result[rowIndex][buttonIndex] = button.Text
		}
	}
	return result
}

func oneButton(button telegramui.Button) telegramui.CardKeyboard {
	return telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{button}}}
}
