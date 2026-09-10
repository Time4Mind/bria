// Package telegramsemantic validates transport-neutral session and menu actions.
package telegramsemantic

import (
	"bria/internal/telegramcontrolport"
	"bria/internal/telegramsettingsview"
	"errors"
	"fmt"
)

type SemanticActionKind = telegramcontrolport.SemanticActionKind
type SemanticAction = telegramcontrolport.SemanticAction

const (
	SemanticSettingsHiddenDirectories        SemanticActionKind = "settings_hidden_directories"
	SemanticPagePrevious                     SemanticActionKind = "page_previous"
	SemanticPageLatest                       SemanticActionKind = "page_latest"
	SemanticPageNext                         SemanticActionKind = "page_next"
	SemanticStop                             SemanticActionKind = "stop"
	SemanticClose                            SemanticActionKind = "close"
	SemanticOptions                          SemanticActionKind = "options"
	SemanticModelMenu                        SemanticActionKind = "model_menu"
	SemanticNativeKey                        SemanticActionKind = "native_key"
	SemanticModelChoice                      SemanticActionKind = "model_choice"
	SemanticEffortMenu                       SemanticActionKind = "effort_menu"
	SemanticEffortChoice                     SemanticActionKind = "effort_choice"
	SemanticScreen                           SemanticActionKind = "screen"
	SemanticSelect                           SemanticActionKind = "select_session"
	SemanticResume                           SemanticActionKind = "resume"
	SemanticMenuSessions                     SemanticActionKind = "menu_sessions"
	SemanticMenuNew                          SemanticActionKind = "menu_new"
	SemanticMenuArchive                      SemanticActionKind = "menu_archive"
	SemanticMenuStatus                       SemanticActionKind = "menu_status"
	SemanticRefreshStatus                    SemanticActionKind = "refresh_status"
	SemanticMenuSettings                     SemanticActionKind = "menu_settings"
	SemanticMenuBack                         SemanticActionKind = "menu_back"
	SemanticMenuNodes                        SemanticActionKind = "menu_nodes"
	SemanticSelectNode                       SemanticActionKind = "select_node"
	SemanticCreateSelectCodex                SemanticActionKind = "create_select_codex"
	SemanticCreateSelectClaude               SemanticActionKind = "create_select_claude"
	SemanticCreateWorkdir                    SemanticActionKind = "create_workdir"
	SemanticCreateConfirm                    SemanticActionKind = "create_confirm"
	SemanticCreateChoice                     SemanticActionKind = "create_choice"
	SemanticCreatePrevious                   SemanticActionKind = "create_previous"
	SemanticCreateFirst                      SemanticActionKind = "create_first"
	SemanticCreateNext                       SemanticActionKind = "create_next"
	SemanticCreateUp                         SemanticActionKind = "create_up"
	SemanticCreatePick                       SemanticActionKind = "create_pick"
	SemanticCreateDirectoryNew               SemanticActionKind = "create_directory_new"
	SemanticCreateBack                       SemanticActionKind = "create_back"
	SemanticCreateFresh                      SemanticActionKind = "create_fresh"
	SemanticCreateCodex                      SemanticActionKind = "create_codex"
	SemanticCreateClaude                     SemanticActionKind = "create_claude"
	SemanticSettingsCategory                 SemanticActionKind = "settings_category"
	SemanticSettingsScreen                   SemanticActionKind = "settings_screen"
	SemanticSettingsScreenCaptureLimit       SemanticActionKind = "settings_screen_capture_limit"
	SemanticSettingsAutoApproveCommands      SemanticActionKind = "settings_auto_approve_commands"
	SemanticSettingsDetail                   SemanticActionKind = "settings_detail"
	SemanticSettingsPageLimit                SemanticActionKind = "settings_page_limit"
	SemanticSettingsContinueExisting         SemanticActionKind = "settings_continue_existing"
	SemanticSettingsTechnicalActions         SemanticActionKind = "settings_technical_actions"
	SemanticSettingsTechnicalOutputLines     SemanticActionKind = "settings_technical_output_lines"
	SemanticSettingsTechnicalCommandLines    SemanticActionKind = "settings_technical_command_lines"
	SemanticSettingsBackgroundQuestions      SemanticActionKind = "settings_background_questions"
	SemanticSettingsBackgroundErrors         SemanticActionKind = "settings_background_errors"
	SemanticSettingsArchiveRecommendations   SemanticActionKind = "settings_archive_recommendations"
	SemanticSettingsDefaultProvider          SemanticActionKind = "settings_default_provider"
	SemanticSettingsDefaultWorkdir           SemanticActionKind = "settings_default_workdir"
	SemanticSettingsClearCreationDefaults    SemanticActionKind = "settings_clear_creation_defaults"
	SemanticSettingsLifetimeNever            SemanticActionKind = "settings_lifetime_never"
	SemanticSettingsLifetime6Hours           SemanticActionKind = "settings_lifetime_6h"
	SemanticSettingsLifetime12Hours          SemanticActionKind = "settings_lifetime_12h"
	SemanticSettingsLifetime24Hours          SemanticActionKind = "settings_lifetime_24h"
	SemanticSettingsLifetime48Hours          SemanticActionKind = "settings_lifetime_48h"
	SemanticSettingsProviderCodex            SemanticActionKind = "settings_provider_codex"
	SemanticSettingsProviderClaude           SemanticActionKind = "settings_provider_claude"
	SemanticSettingsPreprocessing            SemanticActionKind = "settings_preprocessing"
	SemanticSettingsPreprocessingDisabled    SemanticActionKind = "settings_preprocessing_disabled"
	SemanticSettingsPreprocessingShared      SemanticActionKind = "settings_preprocessing_shared"
	SemanticSettingsPreprocessingPerSession  SemanticActionKind = "settings_preprocessing_per_session"
	SemanticSettingsPreprocessingInstruction SemanticActionKind = "settings_preprocessing_instruction"
	SemanticSettingsPreprocessingReset       SemanticActionKind = "settings_preprocessing_reset"
	SemanticSettingsSessionNaming            SemanticActionKind = "settings_session_naming"
	SemanticSettingsStandby                  SemanticActionKind = "settings_standby"
	SemanticSettingsRenameNode               SemanticActionKind = "settings_rename_node"
	SemanticAuthorizeCodex                   SemanticActionKind = "authorize_codex"
	SemanticAuthorizeClaude                  SemanticActionKind = "authorize_claude"
)

func IsGlobal(kind SemanticActionKind) bool {
	switch kind {
	case SemanticMenuSessions, SemanticMenuNew, SemanticMenuArchive, SemanticMenuStatus, SemanticRefreshStatus, SemanticMenuNodes, SemanticSelectNode,
		SemanticMenuSettings, SemanticMenuBack, SemanticCreateSelectCodex, SemanticCreateSelectClaude,
		SemanticCreateWorkdir, SemanticCreateConfirm, SemanticCreateCodex, SemanticCreateClaude,
		SemanticCreateChoice, SemanticCreatePrevious, SemanticCreateFirst, SemanticCreateNext,
		SemanticCreateUp, SemanticCreatePick, SemanticCreateDirectoryNew, SemanticCreateBack, SemanticCreateFresh,
		SemanticSettingsCategory, SemanticSettingsScreen, SemanticSettingsScreenCaptureLimit, SemanticSettingsAutoApproveCommands, SemanticSettingsDetail, SemanticSettingsPageLimit, SemanticSettingsContinueExisting,
		SemanticSettingsTechnicalActions, SemanticSettingsTechnicalOutputLines, SemanticSettingsTechnicalCommandLines, SemanticSettingsBackgroundQuestions, SemanticSettingsBackgroundErrors,
		SemanticSettingsArchiveRecommendations, SemanticSettingsHiddenDirectories,
		SemanticSettingsDefaultProvider, SemanticSettingsDefaultWorkdir, SemanticSettingsClearCreationDefaults,
		SemanticSettingsLifetimeNever, SemanticSettingsLifetime6Hours, SemanticSettingsLifetime12Hours,
		SemanticSettingsLifetime24Hours, SemanticSettingsLifetime48Hours,
		SemanticSettingsProviderCodex, SemanticSettingsProviderClaude,
		SemanticSettingsPreprocessing, SemanticSettingsPreprocessingDisabled, SemanticSettingsPreprocessingShared, SemanticSettingsPreprocessingPerSession,
		SemanticSettingsPreprocessingInstruction, SemanticSettingsPreprocessingReset,
		SemanticSettingsSessionNaming, SemanticSettingsStandby,
		SemanticSettingsRenameNode,
		SemanticAuthorizeCodex, SemanticAuthorizeClaude:
		return true
	}
	return false
}

func ValidateAction(action SemanticAction) error {
	if IsGlobal(action.Kind) {
		// Nodes and Back may originate on a session card. Preserve that
		// session identity so navigation can return to the exact card.
		allowSession := action.Kind == SemanticMenuNodes || action.Kind == SemanticMenuBack
		if (!allowSession && action.SessionID != "") || action.Page != 0 || action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("global semantic action must not contain a session or target fields")
		}
		if action.Kind == SemanticCreateChoice || action.Kind == SemanticSettingsCategory || action.Kind == SemanticSelectNode {
			if action.Choice <= 0 {
				return errors.New("global choice must be positive")
			}
			if action.Kind == SemanticSettingsCategory && action.Choice > int(telegramsettingsview.CategoryProviders) {
				return errors.New("settings category is invalid")
			}
		} else if action.Kind == SemanticMenuArchive {
			if action.Choice < 0 {
				return errors.New("archive page must not be negative")
			}
		} else if action.Choice != 0 {
			return errors.New("global semantic action must not contain a choice")
		}
		if (action.Kind == SemanticCreateConfirm || action.Kind == SemanticCreateCodex || action.Kind == SemanticCreateClaude || action.Kind == SemanticAuthorizeCodex || action.Kind == SemanticAuthorizeClaude) && action.UpdateID <= 0 {
			return errors.New("side-effecting global action requires the claimed Telegram update id")
		}
		return nil
	}
	if action.SessionID == "" {
		return errors.New("semantic action session id is required")
	}
	switch action.Kind {
	case SemanticNativeKey:
		if action.Choice < 1 || action.Choice > 8 || action.Page != 0 || action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("native key target is invalid")
		}
	case SemanticModelMenu, SemanticEffortMenu, SemanticModelChoice, SemanticEffortChoice:
		if action.Page != 0 || action.FollowLatest || action.SessionSlot != 0 || action.Choice < 0 || ((action.Kind == SemanticModelChoice || action.Kind == SemanticEffortChoice) && action.Choice == 0) {
			return errors.New("model action target is invalid")
		}
	case SemanticPagePrevious, SemanticPageNext:
		if action.Page < 1 || action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("semantic page action target is invalid")
		}
	case SemanticPageLatest:
		if action.Page != 0 || !action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("semantic latest-page target is invalid")
		}
	case SemanticStop, SemanticOptions, SemanticScreen, SemanticSelect, SemanticResume:
		if action.Page != 0 || action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("semantic non-page action must not contain target fields")
		}
	case SemanticClose:
		if action.Page != 0 || action.FollowLatest || action.SessionSlot != 0 || action.Choice < 0 || action.Choice > 2 {
			return errors.New("semantic close action target is invalid")
		}
	default:
		return fmt.Errorf("unsupported semantic action %q", action.Kind)
	}
	return nil
}
