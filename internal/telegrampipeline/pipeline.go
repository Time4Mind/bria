package telegrampipeline

import (
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramrecovery"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrNotOwner         = errors.New("Telegram callback is not owned by this user")
	ErrNotPrivate       = errors.New("Telegram callback is not from the owner's private chat")
	ErrStaleCallback    = errors.New("Telegram callback is stale")
	ErrReplayedCallback = errors.New("Telegram callback was already used")
	ErrUnknownOperation = errors.New("outbound operation is unknown")
)

type CallbackDecoder interface {
	DecodeCallbackWithMetadata(string) (telegrambridge.DecodedCallback, error)
}

type CallbackPresentation struct {
	SessionID            domain.SessionID
	Carrier              telegramstate.Carrier
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
	SessionID   domain.SessionID
	Carrier     telegramstate.Carrier
	Phase       string
}
type AcceptedTurnRecoveryBinding = telegramrecovery.AcceptedTurnBinding
type StatusRecoveryBinding = statusrecovery.Binding
type ArtifactRetryBinding = telegrambridge.ArtifactRetryBinding
type CallbackClaim struct {
	SessionID       domain.SessionID
	Carrier         telegramstate.Carrier
	TokenID         string
	ExpiresAt       time.Time
	UpdateID        int64
	CallbackQueryID string
}
type CallbackClaimResult struct {
	Outcome               ClaimOutcome
	PresentationSessionID domain.SessionID
	InteractionRequestID  string
	OutboundOperationID   string
	OutboundUpdateID      int64
	Recovery              *CallbackRecoveryBinding
	AcceptedTurnRecovery  *AcceptedTurnRecoveryBinding
	StatusRecovery        *StatusRecoveryBinding
	ArtifactRetry         *ArtifactRetryBinding
}
type ClaimOutcome string

const (
	ClaimAccepted  ClaimOutcome = "accepted"
	ClaimRecovered ClaimOutcome = "recovered"
	ClaimStale     ClaimOutcome = "stale"
	ClaimReplayed  ClaimOutcome = "replayed"
)

type CallbackRegistry interface {
	Replace(context.Context, CallbackPresentation) error
	Claim(context.Context, CallbackClaim) (CallbackClaimResult, error)
	InvalidateCarrier(context.Context, telegramstate.Carrier) error
}

func BindPresentation(
	ctx context.Context,
	registry CallbackRegistry,
	carrier telegramstate.Carrier,
	presentation telegrambridge.KeyboardPresentation,
) error {
	if registry == nil {
		return errors.New("callback registry is required")
	}
	if carrier.ChatID <= 0 || carrier.MessageID <= 0 {
		return errors.New("confirmed callback carrier is required")
	}
	return registry.Replace(ctx, CallbackPresentation{
		SessionID:            domain.SessionID(presentation.SessionID),
		Carrier:              carrier,
		TokenIDs:             append([]string(nil), presentation.TokenIDs...),
		ExpiresAt:            presentation.ExpiresAt,
		InteractionRequestID: presentation.InteractionRequestID,
		OutboundOperationID:  presentation.OutboundOperationID,
		OutboundUpdateID:     presentation.OutboundUpdateID,
		Recovery:             callbackRecoveryFromPresentation(presentation.Recovery),
		AcceptedTurnRecovery: acceptedTurnRecoveryFromPresentation(presentation.AcceptedTurnRecovery),
		StatusRecovery:       statusRecoveryFromPresentation(presentation.StatusRecovery),
		ArtifactRetry:        cloneArtifactRetryBinding(presentation.ArtifactRetry),
	})
}
func statusRecoveryFromPresentation(binding *telegrambridge.StatusRecoveryBinding) *StatusRecoveryBinding {
	if binding == nil {
		return nil
	}
	clone := *binding
	return &clone
}
func acceptedTurnRecoveryFromPresentation(binding *telegrambridge.AcceptedTurnRecoveryBinding) *AcceptedTurnRecoveryBinding {
	if binding == nil {
		return nil
	}
	return telegramrecovery.CloneAcceptedTurnBinding(binding)
}
func callbackRecoveryFromPresentation(binding *telegrambridge.CallbackRecoveryBinding) *CallbackRecoveryBinding {
	if binding == nil {
		return nil
	}
	return &CallbackRecoveryBinding{
		OperationID: binding.OperationID, UpdateID: binding.UpdateID, SessionID: domain.SessionID(binding.SessionID),
		Carrier: telegramstate.Carrier{ChatID: binding.ChatID, MessageID: binding.MessageID}, Phase: binding.Phase,
	}
}

type AcceptedCallback struct {
	UpdateID             int64
	SessionID            domain.SessionID
	Carrier              telegramstate.Carrier
	Action               telegramui.Action
	Target               telegramui.ButtonTarget
	InteractionRequestID string
	OutboundOperationID  string
	OutboundUpdateID     int64
	Recovery             *CallbackRecoveryBinding
	AcceptedTurnRecovery *AcceptedTurnRecoveryBinding
	StatusRecovery       *StatusRecoveryBinding
	ArtifactRetry        *ArtifactRetryBinding
}

func AcceptCallback(
	ctx context.Context,
	update coordinator.Update,
	ownerUserID, ownerPrivateChatID int64,
	cards CardStore,
	registry CallbackRegistry,
	decoder CallbackDecoder,
) (AcceptedCallback, error) {
	return acceptCallback(ctx, update, ownerUserID, ownerPrivateChatID, cards, registry, decoder, false)
}

func AcceptCallbackForDurableOperation(
	ctx context.Context,
	update coordinator.Update,
	ownerUserID, ownerPrivateChatID int64,
	cards CardStore,
	registry CallbackRegistry,
	decoder CallbackDecoder,
) (AcceptedCallback, error) {
	return acceptCallback(ctx, update, ownerUserID, ownerPrivateChatID, cards, registry, decoder, true)
}
func acceptCallback(
	ctx context.Context,
	update coordinator.Update,
	ownerUserID, ownerPrivateChatID int64,
	cards CardStore,
	registry CallbackRegistry,
	decoder CallbackDecoder,
	allowExactRecovery bool,
) (AcceptedCallback, error) {
	if decoder == nil {
		return AcceptedCallback{}, errors.New("callback decoder is required")
	}
	if registry == nil {
		return AcceptedCallback{}, errors.New("callback registry is required")
	}
	if update.ID <= 0 || update.Text == "" {
		return AcceptedCallback{}, errors.New("callback update identity and data are required")
	}
	decoded, err := decoder.DecodeCallbackWithMetadata(update.Text)
	if err != nil {
		return AcceptedCallback{}, err
	}
	if err := validateCallbackOrigin(update, ownerUserID, ownerPrivateChatID); err != nil {
		return AcceptedCallback{}, err
	}
	claim := CallbackClaim{
		SessionID:       domain.SessionID(decoded.Callback.SessionID),
		Carrier:         telegramstate.Carrier{ChatID: update.ConversationID, MessageID: update.SourceMessageID},
		TokenID:         decoded.TokenID,
		ExpiresAt:       decoded.ExpiresAt,
		UpdateID:        update.ID,
		CallbackQueryID: update.CallbackQueryID,
	}
	claimResult, err := registry.Claim(ctx, claim)
	if err != nil {
		return AcceptedCallback{}, fmt.Errorf("claim Telegram callback: %w", err)
	}
	switch claimResult.Outcome {
	case ClaimAccepted:
	case ClaimRecovered:
		if !allowExactRecovery {
			return AcceptedCallback{}, ErrReplayedCallback
		}
	case ClaimStale:
		return AcceptedCallback{}, ErrStaleCallback
	case ClaimReplayed:
		return AcceptedCallback{}, ErrReplayedCallback
	default:
		return AcceptedCallback{}, errors.New("callback registry returned an unknown claim outcome")
	}
	ownerIsGlobal := claimResult.PresentationSessionID == domain.SessionID(telegramui.GlobalSurfaceID)
	if ownerIsGlobal {
		recoveryBound := claimResult.Recovery != nil
		outboundBound := claimResult.OutboundOperationID != "" || claimResult.OutboundUpdateID != 0
		validGlobal := telegramui.IsGlobalAction(decoded.Callback.Action) && !telegramui.IsOutboundResolutionAction(decoded.Callback.Action) &&
			!telegramui.IsCallbackRecoveryAction(decoded.Callback.Action) && !telegramui.IsStatusRecoveryAction(decoded.Callback.Action) &&
			decoded.Callback.SessionID == telegramui.GlobalSurfaceID
		if outboundBound {
			validGlobal = telegramui.IsOutboundResolutionAction(decoded.Callback.Action) &&
				claimResult.OutboundOperationID != "" && claimResult.OutboundUpdateID > 0 && decoded.Callback.SessionID == telegramui.GlobalSurfaceID
		}
		if recoveryBound {
			validGlobal = telegramui.IsCallbackRecoveryAction(decoded.Callback.Action) && decoded.Callback.SessionID == telegramui.GlobalSurfaceID
		}
		statusRecoveryBound := claimResult.StatusRecovery != nil
		if statusRecoveryBound {
			validGlobal = telegramui.IsStatusRecoveryAction(decoded.Callback.Action) && decoded.Callback.SessionID == telegramui.GlobalSurfaceID
		}
		validSessionTarget := (decoded.Callback.Action == telegramui.ActionSelectSession || decoded.Callback.Action == telegramui.ActionResume) &&
			decoded.Callback.SessionID != telegramui.GlobalSurfaceID && !outboundBound && !recoveryBound && !statusRecoveryBound
		if !validGlobal && !validSessionTarget {
			return AcceptedCallback{}, ErrStaleCallback
		}
		return AcceptedCallback{
			UpdateID: update.ID, SessionID: domain.SessionID(decoded.Callback.SessionID),
			Carrier: claim.Carrier, Action: decoded.Callback.Action, Target: decoded.Callback.Target,
			InteractionRequestID: claimResult.InteractionRequestID,
			OutboundOperationID:  claimResult.OutboundOperationID, OutboundUpdateID: claimResult.OutboundUpdateID,
			Recovery:       cloneCallbackRecoveryBinding(claimResult.Recovery),
			StatusRecovery: cloneStatusRecoveryBinding(claimResult.StatusRecovery),
		}, nil
	}
	if telegramui.IsGlobalAction(decoded.Callback.Action) {
		return AcceptedCallback{}, ErrStaleCallback
	}
	if claimResult.ArtifactRetry != nil {
		binding := claimResult.ArtifactRetry
		if !telegramui.IsArtifactRetryAction(decoded.Callback.Action) || decoded.Callback.SessionID != binding.PresentationID ||
			claimResult.PresentationSessionID != domain.SessionID(binding.PresentationID) || !validArtifactRetryBinding(binding) {
			return AcceptedCallback{}, ErrStaleCallback
		}
		return AcceptedCallback{UpdateID: update.ID, SessionID: domain.SessionID(binding.SessionID), Carrier: claim.Carrier,
			Action: decoded.Callback.Action, Target: decoded.Callback.Target, ArtifactRetry: cloneArtifactRetryBinding(binding)}, nil
	}
	if telegramui.IsArtifactRetryAction(decoded.Callback.Action) {
		return AcceptedCallback{}, ErrStaleCallback
	}
	if claimResult.AcceptedTurnRecovery != nil {
		binding := claimResult.AcceptedTurnRecovery
		if !telegramui.IsAcceptedTurnRecoveryAction(decoded.Callback.Action) ||
			domain.SessionID(decoded.Callback.SessionID) != binding.SessionID || binding.SessionID != claimResult.PresentationSessionID {
			return AcceptedCallback{}, ErrStaleCallback
		}
		return AcceptedCallback{
			UpdateID: update.ID, SessionID: binding.SessionID, Carrier: claim.Carrier,
			Action: decoded.Callback.Action, Target: decoded.Callback.Target,
			AcceptedTurnRecovery: cloneAcceptedTurnRecoveryBinding(binding),
		}, nil
	}
	if telegramui.IsAcceptedTurnRecoveryAction(decoded.Callback.Action) {
		return AcceptedCallback{}, ErrStaleCallback
	}
	if claimResult.InteractionRequestID != "" {
		if !telegramui.IsInteractionAction(decoded.Callback.Action) ||
			domain.SessionID(decoded.Callback.SessionID) != claimResult.PresentationSessionID {
			return AcceptedCallback{}, ErrStaleCallback
		}
		return AcceptedCallback{
			UpdateID: update.ID, SessionID: domain.SessionID(decoded.Callback.SessionID),
			Carrier: claim.Carrier, Action: decoded.Callback.Action, Target: decoded.Callback.Target,
			InteractionRequestID: claimResult.InteractionRequestID,
		}, nil
	}
	if telegramui.IsInteractionAction(decoded.Callback.Action) {
		return AcceptedCallback{}, ErrStaleCallback
	}
	if cards == nil {
		return AcceptedCallback{}, errors.New("callback card store is required")
	}
	card, ok, err := cards.Load(ctx, claimResult.PresentationSessionID)
	if err != nil {
		return AcceptedCallback{}, fmt.Errorf("load callback card: %w", err)
	}
	if !ok || card.SessionID != claimResult.PresentationSessionID ||
		card.Carrier.ChatID != ownerPrivateChatID || card.Carrier.MessageID != update.SourceMessageID {
		return AcceptedCallback{}, ErrStaleCallback
	}
	return AcceptedCallback{
		UpdateID:             update.ID,
		SessionID:            domain.SessionID(decoded.Callback.SessionID),
		Carrier:              card.Carrier,
		Action:               decoded.Callback.Action,
		Target:               decoded.Callback.Target,
		InteractionRequestID: claimResult.InteractionRequestID,
	}, nil
}
func validateCallbackOrigin(update coordinator.Update, ownerUserID, ownerPrivateChatID int64) error {
	if ownerUserID <= 0 || ownerPrivateChatID <= 0 {
		return errors.New("callback owner identity is required")
	}
	if update.Kind != coordinator.UpdateCallback {
		return errors.New("update is not a callback")
	}
	if update.ActorID != ownerUserID {
		return ErrNotOwner
	}
	if update.ConversationID != ownerPrivateChatID || update.ConversationKind != "private" {
		return ErrNotPrivate
	}
	if update.CallbackQueryID == "" || update.SourceMessageID <= 0 {
		return ErrStaleCallback
	}
	return nil
}

type CallbackEffect string

const (
	EffectProjectPage                          CallbackEffect = "project_page"
	EffectStopSession                          CallbackEffect = "stop_session"
	EffectCloseSession                         CallbackEffect = "close_session"
	EffectToggleOptions                        CallbackEffect = "toggle_options"
	EffectToggleGlobalScreen                   CallbackEffect = "toggle_global_screen"
	EffectSelectSession                        CallbackEffect = "select_session"
	EffectResumeSession                        CallbackEffect = "resume_session"
	EffectOpenSessions                         CallbackEffect = "open_sessions"
	EffectOpenNew                              CallbackEffect = "open_new"
	EffectOpenArchive                          CallbackEffect = "open_archive"
	EffectShowStatus                           CallbackEffect = "show_status"
	EffectOpenSettings                         CallbackEffect = "open_settings"
	EffectOpenMenu                             CallbackEffect = "open_menu"
	EffectCreateCodex                          CallbackEffect = "create_codex"
	EffectCreateClaude                         CallbackEffect = "create_claude"
	EffectToggleSettingsScreen                 CallbackEffect = "toggle_settings_screen"
	EffectToggleSettingsDetail                 CallbackEffect = "toggle_settings_detail"
	EffectAuthorizeCodex                       CallbackEffect = "authorize_codex"
	EffectAuthorizeClaude                      CallbackEffect = "authorize_claude"
	EffectInteractionChoice                    CallbackEffect = "interaction_choice"
	EffectInteractionAccept                    CallbackEffect = "interaction_accept"
	EffectInteractionDecline                   CallbackEffect = "interaction_decline"
	EffectInteractionCancel                    CallbackEffect = "interaction_cancel"
	EffectInteractionOther                     CallbackEffect = "interaction_other"
	EffectOutboundConfirmDelivered             CallbackEffect = "outbound_confirm_delivered"
	EffectOutboundRetryPossibleDuplicate       CallbackEffect = "outbound_retry_possible_duplicate"
	EffectCallbackEffectConfirmed              CallbackEffect = "callback_effect_confirmed"
	EffectCallbackEffectRetryPossibleDuplicate CallbackEffect = "callback_effect_retry_possible_duplicate"
	EffectCallbackSendConfirmed                CallbackEffect = "callback_send_confirmed"
	EffectCallbackSendRetryPossibleDuplicate   CallbackEffect = "callback_send_retry_possible_duplicate"
	EffectAcceptedTurnAssumeCompleted          CallbackEffect = "accepted_turn_assume_completed"
	EffectAcceptedTurnRetryPossibleDuplicate   CallbackEffect = "accepted_turn_retry_possible_duplicate"
	EffectAcceptedTurnCancel                   CallbackEffect = "accepted_turn_cancel"
	EffectStatusRecoveryAssumeDelivered        CallbackEffect = "status_recovery_assume_delivered"
	EffectStatusRecoveryRetryPossibleDuplicate CallbackEffect = "status_recovery_retry_possible_duplicate"
	EffectStatusRecoveryCancel                 CallbackEffect = "status_recovery_cancel"
	EffectArtifactRetry                        CallbackEffect = "artifact_retry"
)

type CallbackPlan struct {
	OperationID          string
	UpdateID             int64
	SessionID            domain.SessionID
	Carrier              telegramstate.Carrier
	Action               telegramui.Action
	Target               telegramui.ButtonTarget
	Effect               CallbackEffect
	Interaction          *CallbackInteraction
	OutboundResolution   *CallbackOutboundResolution
	Recovery             *CallbackRecoveryPlan
	AcceptedTurnRecovery *CallbackAcceptedTurnRecoveryPlan
	StatusRecovery       *CallbackStatusRecoveryPlan
	ArtifactRetry        *CallbackArtifactRetryPlan
}
type CallbackInteraction struct {
	RequestID   string
	ChoiceIndex int
}
type CallbackOutboundResolution struct {
	OperationID string
	UpdateID    int64
	Decision    telegramui.Action
}
type CallbackRecoveryPlan struct {
	OperationID string
	UpdateID    int64
	SessionID   domain.SessionID
	Carrier     telegramstate.Carrier
	Phase       string
	Decision    telegramui.Action
}
type CallbackAcceptedTurnRecoveryPlan struct {
	SessionID         domain.SessionID
	MessageID         string
	BindingGeneration uint64
	Decision          telegramui.Action
}
type CallbackStatusRecoveryPlan struct {
	Binding  StatusRecoveryBinding
	Decision telegramui.Action
}
type CallbackArtifactRetryPlan struct{ Binding ArtifactRetryBinding }

func PlanAcceptedCallback(callback AcceptedCallback) (CallbackPlan, error) {
	if callback.UpdateID <= 0 || callback.SessionID == "" || callback.Carrier.ChatID <= 0 || callback.Carrier.MessageID <= 0 {
		return CallbackPlan{}, errors.New("accepted callback identity is required")
	}
	var effect CallbackEffect
	switch callback.Action {
	case telegramui.ActionPagePrevious, telegramui.ActionPageNext:
		if callback.Target.Page < 1 || callback.Target.FollowLatest || callback.Target.SessionSlot != 0 || callback.Target.InteractionChoice != 0 {
			return CallbackPlan{}, errors.New("page callback target is invalid")
		}
		effect = EffectProjectPage
	case telegramui.ActionPageLatest:
		if callback.Target.Page != 0 || !callback.Target.FollowLatest || callback.Target.SessionSlot != 0 || callback.Target.InteractionChoice != 0 {
			return CallbackPlan{}, errors.New("latest-page callback target is invalid")
		}
		effect = EffectProjectPage
	case telegramui.ActionStop:
		effect = EffectStopSession
	case telegramui.ActionClose:
		effect = EffectCloseSession
	case telegramui.ActionOptions:
		effect = EffectToggleOptions
	case telegramui.ActionScreen:
		effect = EffectToggleGlobalScreen
	case telegramui.ActionSelectSession:
		effect = EffectSelectSession
	case telegramui.ActionResume:
		effect = EffectResumeSession
	case telegramui.ActionMenuSessions:
		effect = EffectOpenSessions
	case telegramui.ActionMenuNew:
		effect = EffectOpenNew
	case telegramui.ActionMenuArchive:
		effect = EffectOpenArchive
	case telegramui.ActionMenuStatus:
		effect = EffectShowStatus
	case telegramui.ActionMenuSettings:
		effect = EffectOpenSettings
	case telegramui.ActionMenuBack:
		effect = EffectOpenMenu
	case telegramui.ActionCreateCodex:
		effect = EffectCreateCodex
	case telegramui.ActionCreateClaude:
		effect = EffectCreateClaude
	case telegramui.ActionSettingsScreen:
		effect = EffectToggleSettingsScreen
	case telegramui.ActionSettingsDetail:
		effect = EffectToggleSettingsDetail
	case telegramui.ActionAuthorizeCodex:
		effect = EffectAuthorizeCodex
	case telegramui.ActionAuthorizeClaude:
		effect = EffectAuthorizeClaude
	case telegramui.ActionInteractionChoice:
		if callback.Target.Page != 0 || callback.Target.FollowLatest || callback.Target.SessionSlot != 0 || callback.Target.InteractionChoice < 1 {
			return CallbackPlan{}, errors.New("interaction choice target is invalid")
		}
		effect = EffectInteractionChoice
	case telegramui.ActionInteractionAccept:
		effect = EffectInteractionAccept
	case telegramui.ActionInteractionDecline:
		effect = EffectInteractionDecline
	case telegramui.ActionInteractionCancel:
		effect = EffectInteractionCancel
	case telegramui.ActionInteractionOther:
		effect = EffectInteractionOther
	case telegramui.ActionOutboundConfirmDelivered:
		effect = EffectOutboundConfirmDelivered
	case telegramui.ActionOutboundRetryPossibleDuplicate:
		effect = EffectOutboundRetryPossibleDuplicate
	case telegramui.ActionCallbackEffectConfirmed:
		effect = EffectCallbackEffectConfirmed
	case telegramui.ActionCallbackEffectRetryPossibleDuplicate:
		effect = EffectCallbackEffectRetryPossibleDuplicate
	case telegramui.ActionCallbackSendConfirmed:
		effect = EffectCallbackSendConfirmed
	case telegramui.ActionCallbackSendRetryPossibleDuplicate:
		effect = EffectCallbackSendRetryPossibleDuplicate
	case telegramui.ActionAcceptedTurnAssumeCompleted:
		effect = EffectAcceptedTurnAssumeCompleted
	case telegramui.ActionAcceptedTurnRetryPossibleDuplicate:
		effect = EffectAcceptedTurnRetryPossibleDuplicate
	case telegramui.ActionAcceptedTurnCancel:
		effect = EffectAcceptedTurnCancel
	case telegramui.ActionStatusRecoveryAssumeDelivered:
		effect = EffectStatusRecoveryAssumeDelivered
	case telegramui.ActionStatusRecoveryRetryPossibleDuplicate:
		effect = EffectStatusRecoveryRetryPossibleDuplicate
	case telegramui.ActionStatusRecoveryCancel:
		effect = EffectStatusRecoveryCancel
	case telegramui.ActionArtifactRetry:
		effect = EffectArtifactRetry
	default:
		return CallbackPlan{}, fmt.Errorf("unsupported callback action %q", callback.Action)
	}
	if effect != EffectProjectPage && effect != EffectInteractionChoice && callback.Target != (telegramui.ButtonTarget{}) {
		return CallbackPlan{}, errors.New("non-page callback must not contain a target")
	}
	global := telegramui.IsGlobalAction(callback.Action)
	if global != (callback.SessionID == domain.SessionID(telegramui.GlobalSurfaceID)) {
		return CallbackPlan{}, errors.New("global callback surface identity is invalid")
	}
	interactionAction := telegramui.IsInteractionAction(callback.Action)
	if interactionAction {
		if callback.InteractionRequestID == "" || len(callback.InteractionRequestID) > 256 || callback.SessionID == domain.SessionID(telegramui.GlobalSurfaceID) {
			return CallbackPlan{}, errors.New("interaction callback request binding is invalid")
		}
	} else if callback.InteractionRequestID != "" {
		return CallbackPlan{}, errors.New("non-interaction callback contains a request binding")
	}
	outboundAction := telegramui.IsOutboundResolutionAction(callback.Action)
	outboundBound := callback.OutboundOperationID != "" || callback.OutboundUpdateID != 0
	if outboundAction {
		if callback.OutboundOperationID == "" || len(callback.OutboundOperationID) > 256 || callback.OutboundUpdateID <= 0 ||
			callback.SessionID != domain.SessionID(telegramui.GlobalSurfaceID) {
			return CallbackPlan{}, errors.New("outbound resolution callback binding is invalid")
		}
	} else if outboundBound {
		return CallbackPlan{}, errors.New("non-resolution callback contains an outbound binding")
	}
	recoveryAction := telegramui.IsCallbackRecoveryAction(callback.Action)
	if recoveryAction {
		if !validCallbackRecoveryBinding(callback.Recovery) || callback.SessionID != domain.SessionID(telegramui.GlobalSurfaceID) {
			return CallbackPlan{}, errors.New("callback recovery binding is invalid")
		}
		effectAction := callback.Action == telegramui.ActionCallbackEffectConfirmed || callback.Action == telegramui.ActionCallbackEffectRetryPossibleDuplicate
		phaseMatches := callback.Recovery.Phase == CallbackEffectUnknownPhase && effectAction ||
			callback.Recovery.Phase == CallbackEffectRetryUnknownPhase && callback.Action == telegramui.ActionCallbackEffectConfirmed ||
			callback.Recovery.Phase == CallbackSendUnknownPhase && !effectAction
		if !phaseMatches {
			return CallbackPlan{}, errors.New("callback recovery action does not match unknown phase")
		}
	} else if callback.Recovery != nil {
		return CallbackPlan{}, errors.New("non-recovery callback contains a recovery binding")
	}
	acceptedTurnAction := telegramui.IsAcceptedTurnRecoveryAction(callback.Action)
	if acceptedTurnAction {
		if !validAcceptedTurnRecoveryBinding(callback.AcceptedTurnRecovery) || callback.SessionID != callback.AcceptedTurnRecovery.SessionID {
			return CallbackPlan{}, errors.New("accepted-turn recovery callback binding is invalid")
		}
	} else if callback.AcceptedTurnRecovery != nil {
		return CallbackPlan{}, errors.New("non-recovery callback contains an accepted-turn binding")
	}
	statusRecoveryAction := telegramui.IsStatusRecoveryAction(callback.Action)
	if statusRecoveryAction {
		if !validStatusRecoveryBinding(callback.StatusRecovery) || callback.SessionID != domain.SessionID(telegramui.GlobalSurfaceID) {
			return CallbackPlan{}, errors.New("status recovery callback binding is invalid")
		}
	} else if callback.StatusRecovery != nil {
		return CallbackPlan{}, errors.New("non-recovery callback contains a status recovery binding")
	}
	artifactRetryAction := telegramui.IsArtifactRetryAction(callback.Action)
	if artifactRetryAction {
		if !validArtifactRetryBinding(callback.ArtifactRetry) || callback.SessionID != domain.SessionID(callback.ArtifactRetry.SessionID) {
			return CallbackPlan{}, errors.New("artifact retry callback binding is invalid")
		}
	} else if callback.ArtifactRetry != nil {
		return CallbackPlan{}, errors.New("non-artifact callback contains an artifact retry binding")
	}
	var interaction *CallbackInteraction
	if interactionAction {
		interaction = &CallbackInteraction{RequestID: callback.InteractionRequestID, ChoiceIndex: callback.Target.InteractionChoice}
	}
	var outboundResolution *CallbackOutboundResolution
	if outboundAction {
		outboundResolution = &CallbackOutboundResolution{
			OperationID: callback.OutboundOperationID, UpdateID: callback.OutboundUpdateID, Decision: callback.Action,
		}
	}
	var recovery *CallbackRecoveryPlan
	if recoveryAction {
		recovery = &CallbackRecoveryPlan{
			OperationID: callback.Recovery.OperationID, UpdateID: callback.Recovery.UpdateID,
			SessionID: callback.Recovery.SessionID, Carrier: callback.Recovery.Carrier,
			Phase: callback.Recovery.Phase, Decision: callback.Action,
		}
	}
	var acceptedTurnRecovery *CallbackAcceptedTurnRecoveryPlan
	if acceptedTurnAction {
		acceptedTurnRecovery = &CallbackAcceptedTurnRecoveryPlan{
			SessionID: callback.AcceptedTurnRecovery.SessionID, MessageID: callback.AcceptedTurnRecovery.MessageID,
			BindingGeneration: callback.AcceptedTurnRecovery.BindingGeneration, Decision: callback.Action,
		}
	}
	var statusRecovery *CallbackStatusRecoveryPlan
	if statusRecoveryAction {
		statusRecovery = &CallbackStatusRecoveryPlan{Binding: *cloneStatusRecoveryBinding(callback.StatusRecovery), Decision: callback.Action}
	}
	var artifactRetry *CallbackArtifactRetryPlan
	if artifactRetryAction {
		artifactRetry = &CallbackArtifactRetryPlan{Binding: *cloneArtifactRetryBinding(callback.ArtifactRetry)}
	}
	return CallbackPlan{
		OperationID:          "status:" + strconv.FormatInt(callback.UpdateID, 10),
		UpdateID:             callback.UpdateID,
		SessionID:            callback.SessionID,
		Carrier:              callback.Carrier,
		Action:               callback.Action,
		Target:               callback.Target,
		Effect:               effect,
		Interaction:          interaction,
		OutboundResolution:   outboundResolution,
		Recovery:             recovery,
		AcceptedTurnRecovery: acceptedTurnRecovery,
		StatusRecovery:       statusRecovery,
		ArtifactRetry:        artifactRetry,
	}, nil
}

type CardStore interface {
	Load(context.Context, domain.SessionID) (telegramstate.Card, bool, error)
}

type StateCardStore struct{ store telegramstate.Store }

func NewStateCardStore(store telegramstate.Store) (StateCardStore, error) {
	if store == nil {
		return StateCardStore{}, errors.New("Telegram UI state store is required")
	}
	return StateCardStore{store: store}, nil
}
func (store StateCardStore) Load(ctx context.Context, id domain.SessionID) (telegramstate.Card, bool, error) {
	if store.store == nil {
		return telegramstate.Card{}, false, errors.New("Telegram UI state store is required")
	}
	state, err := store.store.Load(ctx)
	if err != nil {
		return telegramstate.Card{}, false, err
	}
	card, ok := state.Card(id)
	return card, ok, nil
}

type CallbackInput struct {
	Update   coordinator.Update
	Callback telegrambridge.Callback
}

func ValidateCallback(ctx context.Context, input CallbackInput, ownerUserID, ownerPrivateChatID int64, cards CardStore) (telegramstate.Card, error) {
	if err := ctx.Err(); err != nil {
		return telegramstate.Card{}, err
	}
	if err := validateCallbackOrigin(input.Update, ownerUserID, ownerPrivateChatID); err != nil {
		return telegramstate.Card{}, err
	}
	if cards == nil {
		return telegramstate.Card{}, errors.New("callback card store is required")
	}
	card, ok, err := cards.Load(ctx, domain.SessionID(input.Callback.SessionID))
	if err != nil {
		return telegramstate.Card{}, fmt.Errorf("load callback card: %w", err)
	}
	if !ok || card.SessionID != domain.SessionID(input.Callback.SessionID) ||
		card.Carrier.ChatID != ownerPrivateChatID || card.Carrier.MessageID != input.Update.SourceMessageID {
		return telegramstate.Card{}, ErrStaleCallback
	}
	return card, nil
}

type Phase string

const (
	Prepared  Phase = "prepared"
	Unknown   Phase = "unknown"
	Confirmed Phase = "confirmed"
)

type Operation struct {
	ID       string
	Kind     string
	UpdateID int64
	Phase    Phase
	Receipt  int64
}
type Journal interface {
	Load(context.Context, string) (Operation, bool, error)
	Prepare(context.Context, Operation) error
	Confirm(context.Context, string, int64) error
	MarkUnknown(context.Context, string) error
}

func Execute(ctx context.Context, journal Journal, operation Operation, call func(context.Context) (int64, error)) (int64, error) {
	if journal == nil || call == nil {
		return 0, errors.New("operation journal and call are required")
	}
	if operation.ID == "" || operation.Kind == "" || operation.UpdateID <= 0 {
		return 0, errors.New("operation identity is required")
	}
	old, found, err := journal.Load(ctx, operation.ID)
	if err != nil {
		return 0, err
	}
	if found {
		switch old.Phase {
		case Confirmed:
			if old.Receipt <= 0 {
				return 0, errors.New("confirmed operation has invalid receipt")
			}
			return old.Receipt, nil
		case Unknown:
			return 0, fmt.Errorf("%w: %s", ErrUnknownOperation, old.ID)
		default:
			return 0, errors.New("operation is already prepared")
		}
	}
	operation.Phase = Prepared
	if err := journal.Prepare(ctx, operation); err != nil {
		return 0, err
	}
	receipt, callErr := call(ctx)
	if callErr != nil || receipt <= 0 {
		_ = journal.MarkUnknown(context.Background(), operation.ID)
		if callErr != nil {
			return 0, fmt.Errorf("external operation %q: %w", operation.ID, callErr)
		}
		return 0, fmt.Errorf("external operation %q returned invalid receipt", operation.ID)
	}
	if err := journal.Confirm(ctx, operation.ID, receipt); err != nil {
		return 0, err
	}
	return receipt, nil
}

type MemoryJournal struct {
	mu         sync.Mutex
	operations map[string]Operation
}
type memoryPresentation struct {
	presentation CallbackPresentation
	available    map[string]struct{}
	claimed      map[string]callbackClaimIdentity
}
type callbackClaimIdentity struct {
	UpdateID        int64
	CallbackQueryID string
}

type MemoryCallbackRegistry struct {
	mu            sync.Mutex
	now           func() time.Time
	presentations map[domain.SessionID]memoryPresentation
}

func NewMemoryCallbackRegistry(now func() time.Time) *MemoryCallbackRegistry {
	if now == nil {
		now = time.Now
	}
	return &MemoryCallbackRegistry{
		now:           now,
		presentations: make(map[domain.SessionID]memoryPresentation),
	}
}
func (registry *MemoryCallbackRegistry) Replace(ctx context.Context, presentation CallbackPresentation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil || registry.now == nil {
		return errors.New("callback registry is required")
	}
	if err := validateCallbackPresentation(presentation, registry.now()); err != nil {
		return err
	}
	available := make(map[string]struct{}, len(presentation.TokenIDs))
	for _, tokenID := range presentation.TokenIDs {
		available[tokenID] = struct{}{}
	}
	copyPresentation := presentation
	copyPresentation.TokenIDs = append([]string(nil), presentation.TokenIDs...)
	copyPresentation.Recovery = cloneCallbackRecoveryBinding(presentation.Recovery)
	copyPresentation.AcceptedTurnRecovery = cloneAcceptedTurnRecoveryBinding(presentation.AcceptedTurnRecovery)
	copyPresentation.StatusRecovery = cloneStatusRecoveryBinding(presentation.StatusRecovery)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for sessionID, candidate := range registry.presentations {
		if sessionID != presentation.SessionID && candidate.presentation.Carrier == presentation.Carrier {
			delete(registry.presentations, sessionID)
		}
	}
	registry.presentations[presentation.SessionID] = memoryPresentation{
		presentation: copyPresentation,
		available:    available,
		claimed:      make(map[string]callbackClaimIdentity),
	}
	return nil
}
func (registry *MemoryCallbackRegistry) Claim(ctx context.Context, claim CallbackClaim) (CallbackClaimResult, error) {
	if err := ctx.Err(); err != nil {
		return CallbackClaimResult{}, err
	}
	if registry == nil || registry.now == nil {
		return CallbackClaimResult{}, errors.New("callback registry is required")
	}
	if err := validateCallbackClaim(claim); err != nil {
		return CallbackClaimResult{}, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	var ownerSessionID domain.SessionID
	var current memoryPresentation
	for sessionID, candidate := range registry.presentations {
		if candidate.presentation.Carrier != claim.Carrier ||
			candidate.presentation.ExpiresAt != claim.ExpiresAt || !candidate.presentation.ExpiresAt.After(registry.now()) {
			continue
		}
		if _, ok := candidate.available[claim.TokenID]; !ok {
			continue
		}
		ownerSessionID = sessionID
		current = candidate
		break
	}
	if ownerSessionID == "" {
		return CallbackClaimResult{Outcome: ClaimStale}, nil
	}
	if identity, used := current.claimed[claim.TokenID]; used {
		if identity.UpdateID == claim.UpdateID && identity.CallbackQueryID == claim.CallbackQueryID {
			return callbackClaimResult(ClaimRecovered, ownerSessionID, current.presentation), nil
		}
		return callbackClaimResult(ClaimReplayed, ownerSessionID, current.presentation), nil
	}
	current.claimed[claim.TokenID] = callbackClaimIdentity{UpdateID: claim.UpdateID, CallbackQueryID: claim.CallbackQueryID}
	registry.presentations[ownerSessionID] = current
	return callbackClaimResult(ClaimAccepted, ownerSessionID, current.presentation), nil
}
func callbackClaimResult(outcome ClaimOutcome, ownerSessionID domain.SessionID, presentation CallbackPresentation) CallbackClaimResult {
	return CallbackClaimResult{
		Outcome: outcome, PresentationSessionID: ownerSessionID,
		InteractionRequestID: presentation.InteractionRequestID,
		OutboundOperationID:  presentation.OutboundOperationID, OutboundUpdateID: presentation.OutboundUpdateID,
		Recovery:             cloneCallbackRecoveryBinding(presentation.Recovery),
		AcceptedTurnRecovery: cloneAcceptedTurnRecoveryBinding(presentation.AcceptedTurnRecovery),
		StatusRecovery:       cloneStatusRecoveryBinding(presentation.StatusRecovery),
		ArtifactRetry:        cloneArtifactRetryBinding(presentation.ArtifactRetry),
	}
}

const (
	CallbackEffectUnknownPhase      = "effect_unknown"
	CallbackEffectRetryUnknownPhase = "effect_retry_unknown"
	CallbackSendUnknownPhase        = "send_unknown"
)

func validCallbackRecoveryBinding(binding *CallbackRecoveryBinding) bool {
	return binding != nil && binding.OperationID != "" && len(binding.OperationID) <= 256 && utf8.ValidString(binding.OperationID) && binding.UpdateID > 0 &&
		binding.SessionID != "" && binding.Carrier.ChatID > 0 && binding.Carrier.MessageID > 0 &&
		(binding.Phase == CallbackEffectUnknownPhase || binding.Phase == CallbackEffectRetryUnknownPhase || binding.Phase == CallbackSendUnknownPhase)
}
func cloneCallbackRecoveryBinding(binding *CallbackRecoveryBinding) *CallbackRecoveryBinding {
	return cloneBinding(binding)
}
func validAcceptedTurnRecoveryBinding(binding *AcceptedTurnRecoveryBinding) bool {
	return telegramrecovery.ValidAcceptedTurnBinding(binding)
}
func cloneAcceptedTurnRecoveryBinding(binding *AcceptedTurnRecoveryBinding) *AcceptedTurnRecoveryBinding {
	return cloneBinding(binding)
}
func validStatusRecoveryBinding(binding *StatusRecoveryBinding) bool {
	return binding != nil && statusrecovery.Valid(*binding)
}
func cloneStatusRecoveryBinding(binding *StatusRecoveryBinding) *StatusRecoveryBinding {
	return cloneBinding(binding)
}
func cloneBinding[T any](binding *T) *T {
	if binding == nil {
		return nil
	}
	clone := *binding
	return &clone
}
func (registry *MemoryCallbackRegistry) InvalidateCarrier(ctx context.Context, carrier telegramstate.Carrier) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil || carrier.ChatID <= 0 || carrier.MessageID <= 0 {
		return errors.New("callback registry and carrier are required")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for sessionID, candidate := range registry.presentations {
		if candidate.presentation.Carrier == carrier {
			delete(registry.presentations, sessionID)
		}
	}
	return nil
}
func NewMemoryJournal() *MemoryJournal { return &MemoryJournal{operations: make(map[string]Operation)} }
func (j *MemoryJournal) Load(ctx context.Context, id string) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	op, ok := j.operations[id]
	return op, ok, nil
}
func (j *MemoryJournal) Prepare(ctx context.Context, op Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.operations[op.ID]; ok {
		return errors.New("operation already exists")
	}
	j.operations[op.ID] = op
	return nil
}
func (j *MemoryJournal) Confirm(ctx context.Context, id string, receipt int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	op, ok := j.operations[id]
	if !ok {
		return errors.New("operation not found")
	}
	op.Phase = Confirmed
	op.Receipt = receipt
	j.operations[id] = op
	return nil
}
func (j *MemoryJournal) MarkUnknown(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	op, ok := j.operations[id]
	if !ok {
		return errors.New("operation not found")
	}
	op.Phase = Unknown
	op.Receipt = 0
	j.operations[id] = op
	return nil
}
