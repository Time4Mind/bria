// Package telegramcallbackview maps UI buttons and authenticated callback fields.
// It has no token signing, session registry or transport responsibilities.
package telegramcallbackview

import (
	"bria/internal/callbacktoken"
	"bria/internal/telegramui"
	"errors"
	"fmt"
	"strconv"
)

// PresentButton maps a UI button to its label and unsigned callback action/target.
// The caller owns keyboard-wide validation, session binding, expiry and signing.
func PresentButton(button telegramui.Button) (string, callbacktoken.Action, int, error) {
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
			button.Target.SessionSlot != 0 || button.Target.InteractionChoice != 0 || button.Target.Choice != 0 {
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
		if button.Target.Page != 0 || button.Target.FollowLatest || button.Target.SessionSlot != 0 || button.Target.InteractionChoice != 0 || button.Target.Choice < 0 || button.Target.Choice > 2 {
			return "", 0, 0, errors.New("close button target is invalid")
		}
		label := button.Label
		if label == "" {
			label = "Закрыть"
		}
		return label, callbacktoken.ActionClose, button.Target.Choice, nil
	case telegramui.ActionOptions:
		if button.Target != (telegramui.ButtonTarget{}) {
			return "", 0, 0, errors.New("options button must not contain a target")
		}
		return "Опции", callbacktoken.ActionOptions, 0, nil
	case telegramui.ActionScreen:
		if button.Target != (telegramui.ButtonTarget{}) {
			return "", 0, 0, errors.New("screen button must not contain a target")
		}
		return "Скрин", callbacktoken.ActionScreen, 0, nil
	case telegramui.ActionResume:
		if button.Target.Page != 0 || button.Target.FollowLatest || button.Target.SessionSlot < 0 ||
			button.Target.SessionSlot > callbacktoken.MaxTarget || button.Target.InteractionChoice != 0 || button.Target.Choice != 0 {
			return "", 0, 0, errors.New("resume button target is invalid")
		}
		label := button.Label
		if label == "" {
			label = "Продолжить"
		}
		return label, callbacktoken.ActionResume, 0, nil
	case telegramui.ActionNativeKey:
		if button.Target.Page != 0 || button.Target.FollowLatest || button.Target.SessionSlot < 1 ||
			button.Target.InteractionChoice != 0 || button.Target.Choice < 1 || button.Target.Choice > 8 || button.Indicator != nil {
			return "", 0, 0, errors.New("native key button target is invalid")
		}
		return button.Label, callbacktoken.ActionNativeKey, button.Target.Choice, nil
	case telegramui.ActionModelMenu, telegramui.ActionModelChoice, telegramui.ActionEffortMenu, telegramui.ActionEffortChoice:
		if button.Target.Page != 0 || button.Target.FollowLatest || button.Target.SessionSlot < 1 ||
			button.Target.InteractionChoice != 0 || button.Target.Choice < 0 || button.Target.Choice > callbacktoken.MaxTarget || button.Indicator != nil ||
			((button.Action == telegramui.ActionModelChoice || button.Action == telegramui.ActionEffortChoice) && button.Target.Choice == 0) {
			return "", 0, 0, errors.New("model selector button target is invalid")
		}
		actions := map[telegramui.Action]callbacktoken.Action{
			telegramui.ActionModelMenu: callbacktoken.ActionModelMenu, telegramui.ActionModelChoice: callbacktoken.ActionModelChoice,
			telegramui.ActionEffortMenu: callbacktoken.ActionEffortMenu, telegramui.ActionEffortChoice: callbacktoken.ActionEffortChoice,
		}
		return button.Label, actions[button.Action], button.Target.Choice, nil
	case telegramui.ActionMenuSessions:
		return presentGlobalButton(button, "Сессии", callbacktoken.ActionMenuSessions)
	case telegramui.ActionMenuNew:
		return presentGlobalButton(button, "➕ Новая", callbacktoken.ActionMenuNew)
	case telegramui.ActionMenuArchive:
		if button.Target.Page != 0 || button.Target.FollowLatest || button.Target.SessionSlot != 0 ||
			button.Target.InteractionChoice != 0 || button.Target.Choice < 0 || button.Target.Choice > callbacktoken.MaxTarget || button.Indicator != nil {
			return "", 0, 0, errors.New("archive button target is invalid")
		}
		label := button.Label
		if label == "" {
			label = "Архив"
		}
		return label, callbacktoken.ActionMenuArchive, button.Target.Choice, nil
	case telegramui.ActionMenuStatus:
		return presentGlobalButton(button, "Статус", callbacktoken.ActionMenuStatus)
	case telegramui.ActionRefreshStatus:
		return presentGlobalButton(button, "Обновить", callbacktoken.ActionRefreshStatus)
	case telegramui.ActionMenuSettings:
		label := button.Label
		if label == "" {
			label = "Настройки"
		}
		return presentGlobalButton(button, label, callbacktoken.ActionMenuSettings)
	case telegramui.ActionMenuBack:
		label := button.Label
		if label == "" {
			label = "≡ Меню"
		}
		return presentGlobalButton(button, label, callbacktoken.ActionMenuBack)
	case telegramui.ActionMenuNodes:
		return presentGlobalButton(button, "Ноды", callbacktoken.ActionMenuNodes)
	case telegramui.ActionSelectNode:
		if button.Target.Choice < 1 || button.Target.Choice > callbacktoken.MaxTarget || button.Target.Page != 0 ||
			button.Target.FollowLatest || button.Target.SessionSlot != 0 || button.Target.InteractionChoice != 0 ||
			button.Indicator != nil || button.Label == "" {
			return "", 0, 0, errors.New("node choice button is invalid")
		}
		return button.Label, callbacktoken.ActionSelectNode, button.Target.Choice, nil
	case telegramui.ActionCreateSelectCodex:
		label := button.Label
		if label == "" {
			label = "Codex"
		}
		return presentGlobalButton(button, label, callbacktoken.ActionCreateSelectCodex)
	case telegramui.ActionCreateSelectClaude:
		label := button.Label
		if label == "" {
			label = "Claude"
		}
		return presentGlobalButton(button, label, callbacktoken.ActionCreateSelectClaude)
	case telegramui.ActionCreateWorkdir:
		return presentGlobalButton(button, "Рабочая папка", callbacktoken.ActionCreateWorkdir)
	case telegramui.ActionCreateConfirm:
		return presentGlobalButton(button, "Запустить", callbacktoken.ActionCreateConfirm)
	case telegramui.ActionCreateChoice:
		if button.Target.Choice < 1 || button.Target.Choice > callbacktoken.MaxTarget || button.Target.Page != 0 ||
			button.Target.FollowLatest || button.Target.SessionSlot != 0 || button.Target.InteractionChoice != 0 ||
			button.Indicator != nil || button.Label == "" {
			return "", 0, 0, errors.New("creation choice button is invalid")
		}
		return button.Label, callbacktoken.ActionCreateChoice, button.Target.Choice, nil
	case telegramui.ActionCreatePrevious:
		return presentGlobalButton(button, "◀", callbacktoken.ActionCreatePrevious)
	case telegramui.ActionCreateFirst:
		return presentGlobalButton(button, button.Label, callbacktoken.ActionCreateFirst)
	case telegramui.ActionCreateNext:
		return presentGlobalButton(button, "▶", callbacktoken.ActionCreateNext)
	case telegramui.ActionCreateUp:
		return presentGlobalButton(button, "..", callbacktoken.ActionCreateUp)
	case telegramui.ActionCreatePick:
		return presentGlobalButton(button, "Выбрать", callbacktoken.ActionCreatePick)
	case telegramui.ActionCreateDirectoryNew:
		return presentGlobalButton(button, "Создать папку", callbacktoken.ActionCreateDirectoryNew)
	case telegramui.ActionCreateBack:
		return presentGlobalButton(button, "Назад", callbacktoken.ActionCreateBack)
	case telegramui.ActionCreateFresh:
		return presentGlobalButton(button, "➕ Новая", callbacktoken.ActionCreateFresh)
	case telegramui.ActionCreateCodex:
		return presentGlobalButton(button, "Codex", callbacktoken.ActionCreateCodex)
	case telegramui.ActionCreateClaude:
		return presentGlobalButton(button, "Claude", callbacktoken.ActionCreateClaude)
	case telegramui.ActionSettingsCategory:
		if button.Target.Choice < 1 || button.Target.Choice > callbacktoken.MaxTarget || button.Target.Page != 0 ||
			button.Target.FollowLatest || button.Target.SessionSlot != 0 || button.Target.InteractionChoice != 0 ||
			button.Indicator != nil || button.Label == "" {
			return "", 0, 0, errors.New("settings category button is invalid")
		}
		return button.Label, callbacktoken.ActionSettingsCategory, button.Target.Choice, nil
	case telegramui.ActionSettingsScreen:
		return presentGlobalButton(button, "Скрин", callbacktoken.ActionSettingsScreen)
	case telegramui.ActionSettingsScreenCaptureLimit:
		return presentGlobalButton(button, "Размер захвата скрина", callbacktoken.ActionSettingsScreenCaptureLimit)
	case telegramui.ActionSettingsScreenImageProfile:
		return presentGlobalButton(button, "Качество скрина", callbacktoken.ActionSettingsScreenImageProfile)
	case telegramui.ActionSettingsAutoApproveCommands:
		return presentGlobalButton(button, "Автоподтверждение Codex", callbacktoken.ActionSettingsAutoApproveCommands)
	case telegramui.ActionSettingsDetail:
		return presentGlobalButton(button, "Детализация", callbacktoken.ActionSettingsDetail)
	case telegramui.ActionSettingsPageLimit:
		return presentGlobalButton(button, "Страницы", callbacktoken.ActionSettingsPageLimit)
	case telegramui.ActionSettingsContinueExisting:
		return presentGlobalButton(button, "Продолжение", callbacktoken.ActionSettingsContinueExisting)
	case telegramui.ActionSettingsTechnicalActions:
		return presentGlobalButton(button, "Тех. действия", callbacktoken.ActionSettingsTechnicalActions)
	case telegramui.ActionSettingsTechnicalOutputLines:
		return presentGlobalButton(button, "Строки технического вывода", callbacktoken.ActionSettingsTechnicalOutputLines)
	case telegramui.ActionSettingsTechnicalCommandLines:
		return presentGlobalButton(button, "Строки команды", callbacktoken.ActionSettingsTechnicalCommandLines)
	case telegramui.ActionSettingsHiddenDirectories:
		return presentGlobalButton(button, "Скрытые каталоги", callbacktoken.ActionSettingsHiddenDirectories)
	case telegramui.ActionSettingsBackgroundQuestions:
		return presentGlobalButton(button, "Вопросы", callbacktoken.ActionSettingsBackgroundQuestions)
	case telegramui.ActionSettingsBackgroundErrors:
		return presentGlobalButton(button, "Ошибки", callbacktoken.ActionSettingsBackgroundErrors)
	case telegramui.ActionSettingsArchiveRecommendations:
		return presentGlobalButton(button, "Рекомендации архива", callbacktoken.ActionSettingsArchiveRecommendations)
	case telegramui.ActionSettingsDefaultProvider:
		return presentGlobalButton(button, "Backend по умолчанию", callbacktoken.ActionSettingsDefaultProvider)
	case telegramui.ActionSettingsDefaultWorkdir:
		return presentGlobalButton(button, "Папка по умолчанию", callbacktoken.ActionSettingsDefaultWorkdir)
	case telegramui.ActionSettingsClearCreationDefaults:
		return presentGlobalButton(button, "Сбросить defaults", callbacktoken.ActionSettingsClearCreationDefaults)
	case telegramui.ActionSettingsLifetimeNever:
		return presentGlobalButton(button, "Никогда", callbacktoken.ActionSettingsLifetimeNever)
	case telegramui.ActionSettingsLifetime6Hours:
		return presentGlobalButton(button, "6 ч", callbacktoken.ActionSettingsLifetime6Hours)
	case telegramui.ActionSettingsLifetime12Hours:
		return presentGlobalButton(button, "12 ч", callbacktoken.ActionSettingsLifetime12Hours)
	case telegramui.ActionSettingsLifetime24Hours:
		return presentGlobalButton(button, "24 ч", callbacktoken.ActionSettingsLifetime24Hours)
	case telegramui.ActionSettingsLifetime48Hours:
		return presentGlobalButton(button, "48 ч", callbacktoken.ActionSettingsLifetime48Hours)
	case telegramui.ActionSettingsProviderCodex:
		return presentGlobalButton(button, "Codex", callbacktoken.ActionSettingsProviderCodex)
	case telegramui.ActionSettingsProviderClaude:
		return presentGlobalButton(button, "Claude", callbacktoken.ActionSettingsProviderClaude)
	case telegramui.ActionSettingsPreprocessing:
		return presentGlobalButton(button, "Включить / выключить", callbacktoken.ActionSettingsPreprocessing)
	case telegramui.ActionSettingsPreprocessingDisabled:
		return presentGlobalButton(button, modeButtonLabel(button, "Выключен"), callbacktoken.ActionSettingsPreprocessingDisabled)
	case telegramui.ActionSettingsPreprocessingShared:
		return presentGlobalButton(button, modeButtonLabel(button, "Общий"), callbacktoken.ActionSettingsPreprocessingShared)
	case telegramui.ActionSettingsPreprocessingPerSession:
		return presentGlobalButton(button, modeButtonLabel(button, "На сессию"), callbacktoken.ActionSettingsPreprocessingPerSession)
	case telegramui.ActionSettingsPreprocessingInstruction:
		return presentGlobalButton(button, "Изменить инструкцию", callbacktoken.ActionSettingsPreprocessingInstruction)
	case telegramui.ActionSettingsPreprocessingReset:
		return presentGlobalButton(button, "Вернуть встроенную", callbacktoken.ActionSettingsPreprocessingReset)
	case telegramui.ActionSettingsSessionNaming:
		return presentGlobalButton(button, "Автоимя", callbacktoken.ActionSettingsSessionNaming)
	case telegramui.ActionSettingsStandby:
		return presentGlobalButton(button, "Ожидающая сессия", callbacktoken.ActionSettingsStandby)
	case telegramui.ActionSettingsRenameNode:
		return presentGlobalButton(button, "Переименовать ноду", callbacktoken.ActionSettingsRenameNode)
	case telegramui.ActionAuthorizeCodex:
		return presentGlobalButton(button, "Авторизовать Codex", callbacktoken.ActionAuthorizeCodex)
	case telegramui.ActionAuthorizeClaude:
		return presentGlobalButton(button, "Авторизовать Claude", callbacktoken.ActionAuthorizeClaude)
	case telegramui.ActionInteractionChoice:
		if button.Target.Page != 0 || button.Target.FollowLatest || button.Target.SessionSlot != 0 ||
			button.Target.InteractionChoice < 1 || button.Target.InteractionChoice > callbacktoken.MaxTarget || button.Target.Choice != 0 || button.Indicator != nil {
			return "", 0, 0, errors.New("interaction choice button target is invalid")
		}
		return "Вариант " + strconv.Itoa(button.Target.InteractionChoice), callbacktoken.ActionInteractionChoice, button.Target.InteractionChoice, nil
	case telegramui.ActionInteractionAccept:
		return presentGlobalButton(button, "Разрешить", callbacktoken.ActionInteractionAccept)
	case telegramui.ActionInteractionDecline:
		return presentGlobalButton(button, "Отклонить", callbacktoken.ActionInteractionDecline)
	case telegramui.ActionInteractionCancel:
		return presentGlobalButton(button, "Esc", callbacktoken.ActionInteractionCancel)
	case telegramui.ActionInteractionOther:
		return presentGlobalButton(button, "Другой ответ", callbacktoken.ActionInteractionOther)
	case telegramui.ActionInteractionPrevious:
		return presentGlobalButton(button, "↑", callbacktoken.ActionInteractionPrevious)
	case telegramui.ActionInteractionNext:
		return presentGlobalButton(button, "↓", callbacktoken.ActionInteractionNext)
	case telegramui.ActionInteractionSubmit:
		return presentGlobalButton(button, "↵", callbacktoken.ActionInteractionSubmit)
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
			button.Target.SessionSlot < 1 || button.Target.SessionSlot > callbacktoken.MaxTarget || button.Target.InteractionChoice != 0 || button.Target.Choice != 0 {
			return "", 0, 0, errors.New("session button requires one positive session slot target")
		}
		label := button.Label
		if label == "" {
			label = "Сессия " + strconv.Itoa(button.Target.SessionSlot)
		}
		return label,
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

func modeButtonLabel(button telegramui.Button, fallback string) string {
	if button.Label != "" {
		return button.Label
	}
	return fallback
}

func validPageTarget(target telegramui.ButtonTarget) bool {
	return target.Page >= 1 && target.Page <= callbacktoken.MaxTarget && !target.FollowLatest && target.SessionSlot == 0 && target.InteractionChoice == 0 && target.Choice == 0
}

// DecodeFields projects fields from an already authenticated callback into UI semantics.
// It does not authenticate tokens or validate their expiry; callers must decode with
// callbacktoken.Codec before using this projection to dispatch an action.
func DecodeFields(fields callbacktoken.Fields) (telegramui.Action, telegramui.ButtonTarget, error) {
	switch fields.Action {
	case callbacktoken.ActionModelMenu:
		return telegramui.ActionModelMenu, telegramui.ButtonTarget{Choice: fields.Target}, nil
	case callbacktoken.ActionNativeKey:
		return telegramui.ActionNativeKey, telegramui.ButtonTarget{Choice: fields.Target}, nil
	case callbacktoken.ActionModelChoice:
		return telegramui.ActionModelChoice, telegramui.ButtonTarget{Choice: fields.Target}, nil
	case callbacktoken.ActionEffortMenu:
		return telegramui.ActionEffortMenu, telegramui.ButtonTarget{Choice: fields.Target}, nil
	case callbacktoken.ActionEffortChoice:
		return telegramui.ActionEffortChoice, telegramui.ButtonTarget{Choice: fields.Target}, nil
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
		return telegramui.ActionClose, telegramui.ButtonTarget{Choice: fields.Target}, nil
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
		return telegramui.ActionMenuArchive, telegramui.ButtonTarget{Choice: fields.Target}, nil
	case callbacktoken.ActionMenuStatus:
		return telegramui.ActionMenuStatus, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionRefreshStatus:
		return telegramui.ActionRefreshStatus, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuSettings:
		return telegramui.ActionMenuSettings, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuBack:
		return telegramui.ActionMenuBack, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionMenuNodes:
		return telegramui.ActionMenuNodes, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSelectNode:
		return telegramui.ActionSelectNode, telegramui.ButtonTarget{Choice: fields.Target}, nil
	case callbacktoken.ActionCreateSelectCodex:
		return telegramui.ActionCreateSelectCodex, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateSelectClaude:
		return telegramui.ActionCreateSelectClaude, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateWorkdir:
		return telegramui.ActionCreateWorkdir, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateConfirm:
		return telegramui.ActionCreateConfirm, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateChoice:
		return telegramui.ActionCreateChoice, telegramui.ButtonTarget{Choice: fields.Target}, nil
	case callbacktoken.ActionCreatePrevious:
		return telegramui.ActionCreatePrevious, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateFirst:
		return telegramui.ActionCreateFirst, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateNext:
		return telegramui.ActionCreateNext, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateUp:
		return telegramui.ActionCreateUp, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreatePick:
		return telegramui.ActionCreatePick, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateDirectoryNew:
		return telegramui.ActionCreateDirectoryNew, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateBack:
		return telegramui.ActionCreateBack, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateFresh:
		return telegramui.ActionCreateFresh, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateCodex:
		return telegramui.ActionCreateCodex, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionCreateClaude:
		return telegramui.ActionCreateClaude, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsCategory:
		return telegramui.ActionSettingsCategory, telegramui.ButtonTarget{Choice: fields.Target}, nil
	case callbacktoken.ActionSettingsScreen:
		return telegramui.ActionSettingsScreen, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsScreenCaptureLimit:
		return telegramui.ActionSettingsScreenCaptureLimit, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsScreenImageProfile:
		return telegramui.ActionSettingsScreenImageProfile, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsAutoApproveCommands:
		return telegramui.ActionSettingsAutoApproveCommands, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsDetail:
		return telegramui.ActionSettingsDetail, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsPageLimit:
		return telegramui.ActionSettingsPageLimit, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsContinueExisting:
		return telegramui.ActionSettingsContinueExisting, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsTechnicalActions:
		return telegramui.ActionSettingsTechnicalActions, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsTechnicalOutputLines:
		return telegramui.ActionSettingsTechnicalOutputLines, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsTechnicalCommandLines:
		return telegramui.ActionSettingsTechnicalCommandLines, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsHiddenDirectories:
		return telegramui.ActionSettingsHiddenDirectories, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsBackgroundQuestions:
		return telegramui.ActionSettingsBackgroundQuestions, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsBackgroundErrors:
		return telegramui.ActionSettingsBackgroundErrors, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsArchiveRecommendations:
		return telegramui.ActionSettingsArchiveRecommendations, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsDefaultProvider:
		return telegramui.ActionSettingsDefaultProvider, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsDefaultWorkdir:
		return telegramui.ActionSettingsDefaultWorkdir, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsClearCreationDefaults:
		return telegramui.ActionSettingsClearCreationDefaults, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsLifetimeNever:
		return telegramui.ActionSettingsLifetimeNever, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsLifetime6Hours:
		return telegramui.ActionSettingsLifetime6Hours, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsLifetime12Hours:
		return telegramui.ActionSettingsLifetime12Hours, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsLifetime24Hours:
		return telegramui.ActionSettingsLifetime24Hours, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsLifetime48Hours:
		return telegramui.ActionSettingsLifetime48Hours, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsProviderCodex:
		return telegramui.ActionSettingsProviderCodex, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsProviderClaude:
		return telegramui.ActionSettingsProviderClaude, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsPreprocessing:
		return telegramui.ActionSettingsPreprocessing, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsPreprocessingDisabled:
		return telegramui.ActionSettingsPreprocessingDisabled, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsPreprocessingShared:
		return telegramui.ActionSettingsPreprocessingShared, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsPreprocessingPerSession:
		return telegramui.ActionSettingsPreprocessingPerSession, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsPreprocessingInstruction:
		return telegramui.ActionSettingsPreprocessingInstruction, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsPreprocessingReset:
		return telegramui.ActionSettingsPreprocessingReset, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsSessionNaming:
		return telegramui.ActionSettingsSessionNaming, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsStandby:
		return telegramui.ActionSettingsStandby, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionSettingsRenameNode:
		return telegramui.ActionSettingsRenameNode, telegramui.ButtonTarget{}, nil
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
	case callbacktoken.ActionInteractionPrevious:
		return telegramui.ActionInteractionPrevious, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionInteractionNext:
		return telegramui.ActionInteractionNext, telegramui.ButtonTarget{}, nil
	case callbacktoken.ActionInteractionSubmit:
		return telegramui.ActionInteractionSubmit, telegramui.ButtonTarget{}, nil
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
