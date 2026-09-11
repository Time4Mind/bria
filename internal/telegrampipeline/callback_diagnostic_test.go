package telegrampipeline_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"bria/internal/callbackdiagnostic"
	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func diagnosticCallback(t *testing.T, action telegramui.Action, target telegramui.ButtonTarget) (coordinator.Update, *telegrambridge.Presenter, *telegrampipeline.MemoryCallbackRegistry, telegrambridge.KeyboardPresentation) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	presentation, err := presenter.PresentKeyboardWithManifest(string(sessionID), nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: action, Target: target}}}})
	if err != nil {
		t.Fatal(err)
	}
	registry := telegrampipeline.NewMemoryCallbackRegistry(func() time.Time { return now })
	u := update(7, 42, 99)
	u.ID, u.Text = 101, presentation.Markup.InlineKeyboard[0][0].CallbackData
	return u, presenter, registry, presentation
}

func assertDiagnosticError(t *testing.T, err, sentinel error, message string, want callbackdiagnostic.Details) {
	t.Helper()
	if err == nil || !errors.Is(err, sentinel) || err.Error() != message {
		t.Fatalf("error = %v; want unchanged %q and errors.Is(%v)", err, message, sentinel)
	}
	if got := callbackdiagnostic.Inspect(fmt.Errorf("outer: %w", err)); got != want {
		t.Fatalf("details = %+v, want %+v", got, want)
	}
}

func TestAcceptCallbackDiagnosticMissingPresentation(t *testing.T) {
	u, presenter, registry, presentation := diagnosticCallback(t, telegramui.ActionPageNext, telegramui.ButtonTarget{Page: 2})
	_, err := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, cards{card: card()}, registry, presenter)
	want := callbackdiagnostic.Details{Code: "presentation_missing", Action: "page_next", SessionID: string(sessionID), TokenID: presentation.TokenIDs[0], Target: 2}
	assertDiagnosticError(t, err, telegrampipeline.ErrStaleCallback, "Telegram callback is stale", want)
}

func TestAcceptCallbackDiagnosticReplayAndExactRecovery(t *testing.T) {
	u, presenter, registry, presentation := diagnosticCallback(t, telegramui.ActionPageNext, telegramui.ButtonTarget{Page: 2})
	if err := telegrampipeline.BindPresentation(context.Background(), registry, card().Carrier, presentation); err != nil {
		t.Fatal(err)
	}
	accepted, err := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, cards{card: card()}, registry, presenter)
	if err != nil || accepted.Action != telegramui.ActionPageNext || accepted.Target.Page != 2 {
		t.Fatalf("initial acceptance = %+v, %v", accepted, err)
	}
	want := callbackdiagnostic.Details{Code: "presentation_replayed", Action: "page_next", SessionID: string(sessionID), TokenID: presentation.TokenIDs[0], PresentationID: string(sessionID), Target: 2}
	_, err = telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, cards{card: card()}, registry, presenter)
	assertDiagnosticError(t, err, telegrampipeline.ErrReplayedCallback, "Telegram callback was already used", want)
	distinct := u
	distinct.ID++
	distinct.CallbackQueryID = "other"
	if repeated, repeatErr := telegrampipeline.AcceptCallback(context.Background(), distinct, 7, 42, cards{card: card()}, registry, presenter); repeatErr != nil || repeated.Action != telegramui.ActionPageNext {
		t.Fatalf("distinct rapid navigation callback = %+v, %v", repeated, repeatErr)
	}
	recovered, err := telegrampipeline.AcceptCallbackForDurableOperation(context.Background(), u, 7, 42, cards{card: card()}, registry, presenter)
	if err != nil || recovered != accepted {
		t.Fatalf("durable exact recovery changed: %+v, %v", recovered, err)
	}
}

func TestAcceptCallbackDiagnosticCardRejections(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		store      cardMap
		carrier    telegramstate.Carrier
	}{
		{"missing", "card_missing", cardMap{}, telegramstate.Carrier{}},
		{"wrong message", "card_carrier_mismatch", cardMap{sessionID: {SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 100}}}, telegramstate.Carrier{ChatID: 42, MessageID: 100}},
		{"wrong chat", "card_carrier_mismatch", cardMap{sessionID: {SessionID: sessionID, Carrier: telegramstate.Carrier{ChatID: 43, MessageID: 99}}}, telegramstate.Carrier{ChatID: 43, MessageID: 99}},
		{"wrong session", "callback_binding_mismatch", cardMap{sessionID: {SessionID: domain.SessionID("different"), Carrier: card().Carrier}}, card().Carrier},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, presenter, registry, presentation := diagnosticCallback(t, telegramui.ActionPageNext, telegramui.ButtonTarget{Page: 2})
			if err := telegrampipeline.BindPresentation(context.Background(), registry, card().Carrier, presentation); err != nil {
				t.Fatal(err)
			}
			_, err := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, tc.store, registry, presenter)
			want := callbackdiagnostic.Details{Code: tc.code, Action: "page_next", SessionID: string(sessionID), TokenID: presentation.TokenIDs[0], PresentationID: string(sessionID), Target: 2, CardChatID: tc.carrier.ChatID, CardMessageID: tc.carrier.MessageID}
			assertDiagnosticError(t, err, telegrampipeline.ErrStaleCallback, "Telegram callback is stale", want)
			// A failed card check still consumes the claim, as before logging.
			_, replayErr := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, cards{card: card()}, registry, presenter)
			if !errors.Is(replayErr, telegrampipeline.ErrReplayedCallback) {
				t.Fatalf("claim consumption changed: %v", replayErr)
			}
		})
	}
}

type diagnosticRegistry struct {
	telegrampipeline.CallbackRegistry
	result telegrampipeline.CallbackClaimResult
	err    error
}

func (r diagnosticRegistry) Claim(context.Context, telegrampipeline.CallbackClaim) (telegrampipeline.CallbackClaimResult, error) {
	return r.result, r.err
}

type diagnosticCards struct{ err error }

func (c diagnosticCards) Load(context.Context, domain.SessionID) (telegramstate.Card, bool, error) {
	return telegramstate.Card{}, false, c.err
}

func TestAcceptCallbackDiagnosticOriginAndInfrastructure(t *testing.T) {
	cause := errors.New("infrastructure failure")
	for _, tc := range []struct {
		name, code, message string
		cause               error
	}{
		{"origin", "callback_origin_invalid", "Telegram callback is not owned by this user", telegrampipeline.ErrNotOwner},
		{"registry", "registry_failed", "claim Telegram callback: infrastructure failure", cause},
		{"load", "card_load_failed", "load callback card: infrastructure failure", cause},
		{"cancelled claim", "registry_failed", "claim Telegram callback: context canceled", context.Canceled},
		{"deadline load", "card_load_failed", "load callback card: context deadline exceeded", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, presenter, registry, presentation := diagnosticCallback(t, telegramui.ActionPageNext, telegramui.ButtonTarget{Page: 2})
			if err := telegrampipeline.BindPresentation(context.Background(), registry, card().Carrier, presentation); err != nil {
				t.Fatal(err)
			}
			var useRegistry telegrampipeline.CallbackRegistry = registry
			var useCards telegrampipeline.CardStore = cards{card: card()}
			want := callbackdiagnostic.Details{Code: tc.code, Action: "page_next", SessionID: string(sessionID), TokenID: presentation.TokenIDs[0], Target: 2}
			switch tc.name {
			case "origin":
				u.ActorID++
			case "registry", "cancelled claim":
				useRegistry = diagnosticRegistry{err: tc.cause}
			default:
				useCards = diagnosticCards{err: tc.cause}
				want.PresentationID = string(sessionID)
			}
			_, err := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, useCards, useRegistry, presenter)
			assertDiagnosticError(t, err, tc.cause, tc.message, want)
		})
	}
}

func TestAcceptCallbackDiagnosticBindingMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action telegramui.Action
		result telegrampipeline.CallbackClaimResult
	}{
		{"global action mismatch", telegramui.ActionOptions, telegrampipeline.CallbackClaimResult{PresentationSessionID: domain.SessionID(telegramui.GlobalSurfaceID)}},
		{"artifact binding mismatch", telegramui.ActionOptions, telegrampipeline.CallbackClaimResult{ArtifactRetry: &telegrampipeline.ArtifactRetryBinding{}}},
		{"artifact binding absent", telegramui.ActionArtifactRetry, telegrampipeline.CallbackClaimResult{}},
		{"recovery binding mismatch", telegramui.ActionOptions, telegrampipeline.CallbackClaimResult{AcceptedTurnRecovery: &telegrampipeline.AcceptedTurnRecoveryBinding{}}},
		{"recovery binding absent", telegramui.ActionAcceptedTurnCancel, telegrampipeline.CallbackClaimResult{}},
		{"interaction binding mismatch", telegramui.ActionOptions, telegrampipeline.CallbackClaimResult{InteractionRequestID: "request"}},
		{"interaction binding absent", telegramui.ActionInteractionCancel, telegrampipeline.CallbackClaimResult{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, presenter, _, presentation := diagnosticCallback(t, telegramui.ActionOptions, telegramui.ButtonTarget{})
			wireAction := map[telegramui.Action]callbacktoken.Action{telegramui.ActionArtifactRetry: callbacktoken.ActionArtifactRetry, telegramui.ActionAcceptedTurnCancel: callbacktoken.ActionAcceptedTurnCancel, telegramui.ActionInteractionCancel: callbacktoken.ActionInteractionCancel}[tc.action]
			if wireAction != 0 {
				codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), nil, func() time.Time { return presentation.ExpiresAt.Add(-time.Minute) })
				if err != nil {
					t.Fatal(err)
				}
				u.Text, err = codec.Encode(callbacktoken.Fields{Action: wireAction, SessionID: string(sessionID), ExpiresAt: presentation.ExpiresAt})
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := presenter.DecodeCallbackWithMetadata(u.Text)
				if err != nil {
					t.Fatal(err)
				}
				presentation.TokenIDs = []string{decoded.TokenID}
			}
			result := tc.result
			result.Outcome = telegrampipeline.ClaimAccepted
			if result.PresentationSessionID == "" {
				result.PresentationSessionID = sessionID
			}
			result.Retired = true
			_, err := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, cards{card: card()}, diagnosticRegistry{result: result}, presenter)
			want := callbackdiagnostic.Details{Code: "callback_binding_mismatch", Action: string(tc.action), SessionID: string(sessionID), TokenID: presentation.TokenIDs[0], PresentationID: string(result.PresentationSessionID), Retired: true}
			assertDiagnosticError(t, err, telegrampipeline.ErrStaleCallback, "Telegram callback is stale", want)
		})
	}
}

func TestAcceptCallbackDiagnosticConfigurationFailures(t *testing.T) {
	for _, tc := range []struct{ name, code, message string }{
		{"decoder", "operation_failed", "callback decoder is required"},
		{"registry", "registry_failed", "callback registry is required"},
		{"identity", "callback_origin_invalid", "callback update identity and data are required"},
		{"unknown claim", "registry_failed", "callback registry returned an unknown claim outcome"},
		{"cards", "card_load_failed", "callback card store is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, presenter, registry, presentation := diagnosticCallback(t, telegramui.ActionOptions, telegramui.ButtonTarget{})
			if err := telegrampipeline.BindPresentation(context.Background(), registry, card().Carrier, presentation); err != nil {
				t.Fatal(err)
			}
			var useDecoder telegrampipeline.CallbackDecoder = presenter
			var useRegistry telegrampipeline.CallbackRegistry = registry
			var useCards telegrampipeline.CardStore = cards{card: card()}
			switch tc.name {
			case "decoder":
				useDecoder = nil
			case "registry":
				useRegistry = nil
			case "identity":
				u.ID = 0
			case "unknown claim":
				useRegistry = diagnosticRegistry{}
			case "cards":
				useCards = nil
			}
			_, err := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, useCards, useRegistry, useDecoder)
			if err == nil || err.Error() != tc.message || callbackdiagnostic.Inspect(err).Code != tc.code {
				t.Fatalf("error = %v, details = %+v, want %q/%q", err, callbackdiagnostic.Inspect(err), tc.message, tc.code)
			}
		})
	}
}

type diagnosticDecoder struct{ err error }

func (d diagnosticDecoder) DecodeCallbackWithMetadata(string) (telegrambridge.DecodedCallback, error) {
	// Even partially decoded data must not become trusted failure metadata.
	return telegrambridge.DecodedCallback{Callback: telegrambridge.Callback{SessionID: "untrusted", Action: telegramui.ActionPageNext}, TokenID: "untrusted"}, d.err
}

func TestAcceptCallbackDiagnosticPreservesTokenErrorsWithoutRawData(t *testing.T) {
	u, presenter, registry, _ := diagnosticCallback(t, telegramui.ActionOptions, telegramui.ButtonTarget{})
	u.Text = "raw callback data must not enter diagnostics"
	_, err := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, nil, registry, presenter)
	assertDiagnosticError(t, err, callbacktoken.ErrInvalidToken, "decode Telegram callback: invalid callback token", callbackdiagnostic.Details{})
	for _, sentinel := range []error{callbacktoken.ErrInvalidToken, callbacktoken.ErrExpired, callbacktoken.ErrInvalidFields, callbacktoken.ErrUnsupportedVersion, callbacktoken.ErrUnknownAction} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			cause := fmt.Errorf("decode Telegram callback: %w", sentinel)
			_, err := telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, nil, registry, diagnosticDecoder{err: cause})
			assertDiagnosticError(t, err, sentinel, cause.Error(), callbackdiagnostic.Details{})
		})
	}
}

func TestAcceptCallbackDiagnosticSignedChoiceTarget(t *testing.T) {
	for _, tc := range []struct {
		action callbacktoken.Action
		name   string
	}{{callbacktoken.ActionModelChoice, "model_choice"}, {callbacktoken.ActionInteractionChoice, "interaction_choice"}} {
		t.Run(tc.name, func(t *testing.T) {
			u, presenter, registry, presentation := diagnosticCallback(t, telegramui.ActionOptions, telegramui.ButtonTarget{})
			codec, err := callbacktoken.New(bytes.Repeat([]byte{0x42}, 32), nil, func() time.Time { return presentation.ExpiresAt.Add(-time.Minute) })
			if err != nil {
				t.Fatal(err)
			}
			u.Text, err = codec.Encode(callbacktoken.Fields{Action: tc.action, SessionID: string(sessionID), Target: 3, ExpiresAt: presentation.ExpiresAt})
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := presenter.DecodeCallbackWithMetadata(u.Text)
			if err != nil {
				t.Fatal(err)
			}
			_, err = telegrampipeline.AcceptCallback(context.Background(), u, 7, 42, nil, registry, presenter)
			want := callbackdiagnostic.Details{Code: "presentation_missing", Action: tc.name, SessionID: string(sessionID), TokenID: decoded.TokenID, Target: 3}
			assertDiagnosticError(t, err, telegrampipeline.ErrStaleCallback, "Telegram callback is stale", want)
		})
	}
}
