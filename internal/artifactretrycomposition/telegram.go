package artifactretrycomposition

import (
	"context"
	"errors"
	"strconv"

	"bria/internal/artifactdelivery"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramui"
)

type callbackExecutor struct {
	composition *Composition
	next        telegramflow.CallbackExecutor
}

func WrapCallback(composition *Composition, next telegramflow.CallbackExecutor) telegramflow.CallbackExecutor {
	return &callbackExecutor{composition: composition, next: next}
}

func (executor *callbackExecutor) HandleCallback(ctx context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	if plan.Effect != telegrampipeline.EffectArtifactRetry {
		if executor == nil || executor.next == nil {
			return telegramflow.CallbackResult{}, errors.New("artifact retry fallback executor is required")
		}
		return executor.next.HandleCallback(ctx, plan)
	}
	if executor == nil || executor.composition == nil || plan.ArtifactRetry == nil {
		return telegramflow.CallbackResult{}, ErrStaleDecision
	}
	outcome, err := executor.composition.Retry(ctx, plan.OperationID, bindingFromPipeline(plan.ArtifactRetry.Binding))
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	result := telegramflow.CallbackResult{OperationID: plan.OperationID}
	if outcome.Next == nil {
		result.Terminal = &telegramflow.TerminalOutput{Text: "Доставка файлов завершена."}
		return result, nil
	}
	binding := bindingToPipeline(*outcome.Next)
	result.Surface = &telegramflow.SurfaceOutput{
		Text: summaryText(outcome.Summary), Keyboard: retryKeyboard(), ArtifactRetry: &binding,
	}
	return result, nil
}

type TelegramPublisher struct {
	conversationID int64
	presenter      *telegrambridge.Presenter
	sender         *telegramflow.Sender
}

func NewTelegramPublisher(conversationID int64, presenter *telegrambridge.Presenter, sender *telegramflow.Sender) (*TelegramPublisher, error) {
	if conversationID <= 0 || presenter == nil || sender == nil {
		return nil, ErrInvalidConfiguration
	}
	return &TelegramPublisher{conversationID: conversationID, presenter: presenter, sender: sender}, nil
}

func (publisher *TelegramPublisher) PublishArtifactRetry(ctx context.Context, notice Notice) error {
	if publisher == nil || ctx == nil || notice.OperationID == "" || notice.Sequence == 0 || !validBinding(notice.Binding) {
		return ErrInvalidConfiguration
	}
	binding := bindingToPipeline(notice.Binding)
	prepared, err := telegramflow.PrepareSurface(notice.OperationID, publisher.conversationID, "", 0, false, telegramflow.SurfaceOutput{
		Text: summaryText(notice.Summary), Keyboard: retryKeyboard(), ArtifactRetry: &binding,
	}, publisher.presenter)
	if err != nil {
		return err
	}
	_, err = publisher.sender.EnqueuePrepared(ctx, notice.Sequence, prepared)
	return err
}

func bindingToPipeline(binding Binding) telegrampipeline.ArtifactRetryBinding {
	return telegrampipeline.ArtifactRetryBinding{
		PresentationID: string(binding.PresentationID), SessionID: string(binding.SessionID), MessageID: binding.MessageID,
		FinalOperationID: binding.FinalOperationID, Generation: binding.Generation, Slot: binding.Slot, ExpiresAt: binding.ExpiresAt,
	}
}

func bindingFromPipeline(binding telegrampipeline.ArtifactRetryBinding) Binding {
	return Binding{
		PresentationID:   domain.SessionID(binding.PresentationID),
		SessionID:        domain.SessionID(binding.SessionID),
		MessageID:        binding.MessageID,
		FinalOperationID: binding.FinalOperationID,
		Generation:       binding.Generation,
		Slot:             binding.Slot,
		ExpiresAt:        binding.ExpiresAt,
	}
}

func summaryText(summary artifactdelivery.Summary) string {
	return "Не подтверждена доставка " + strconv.Itoa(summary.Unconfirmed) + " из " + strconv.Itoa(summary.Total) +
		" файлов. Повторить можно только вручную; возможен дубль."
}

func retryKeyboard() telegramui.CardKeyboard {
	return telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionArtifactRetry}}}}
}
