package telegramui

import (
	"fmt"
	"strings"
)

const MaxCallbackBytes = 64

type Action string
type OpaqueToken string

const (
	ActionNoop                 Action = "noop"
	ActionMenu                 Action = "menu"
	ActionSessions             Action = "sessions"
	ActionNodesPrevious        Action = "nodes_prev"
	ActionNodesNext            Action = "nodes_next"
	ActionSessionsPrevious     Action = "sessions_prev"
	ActionSessionsNext         Action = "sessions_next"
	ActionArchive              Action = "archive"
	ActionStatus               Action = "status"
	ActionStatusRefresh        Action = "status_refresh"
	ActionStatusMode           Action = "status_mode"
	ActionStatusLeaderNode     Action = "status_leader"
	ActionStatusSettingsNode   Action = "status_settings"
	ActionNodeSettings         Action = "node_settings"
	ActionConfirmLeader        Action = "confirm_leader"
	ActionClusterAdd           Action = "cluster_add"
	ActionClusterInvite        Action = "cluster_invite"
	ActionClusterContract      Action = "cluster_contract"
	ActionEnrollmentOpen       Action = "enroll_open"
	ActionEnrollmentApprove    Action = "enroll_approve"
	ActionEnrollmentReject     Action = "enroll_reject"
	ActionNodeDisable          Action = "node_disable"
	ActionNodeDisableYes       Action = "node_disable_yes"
	ActionNodeEnable           Action = "node_enable"
	ActionNodeDelete           Action = "node_delete"
	ActionNodeDeleteYes        Action = "node_delete_yes"
	ActionNodeRename           Action = "node_rename"
	ActionProviderAlias        Action = "provider_alias"
	ActionProviderAuth         Action = "provider_auth"
	ActionProviderAuthCancel   Action = "provider_auth_cancel"
	ActionBackendConnect       Action = "backend_connect"
	ActionBackendDisconnect    Action = "backend_remove"
	ActionNewSession           Action = "new"
	ActionNewNode              Action = "new_node"
	ActionNewBackend           Action = "new_backend"
	ActionNewDirectory         Action = "new_dir"
	ActionNewDirectoryUp       Action = "new_up"
	ActionNewDirectoryPick     Action = "new_pick"
	ActionNewDirectoryBack     Action = "new_back"
	ActionNewDirectoryPrev     Action = "new_dprev"
	ActionNewDirectoryFirst    Action = "new_dfirst"
	ActionNewDirectoryNext     Action = "new_dnext"
	ActionNewResume            Action = "new_resume"
	ActionNewResumePrevious    Action = "new_resume_prev"
	ActionNewResumeFirst       Action = "new_resume_first"
	ActionNewResumeNext        Action = "new_resume_next"
	ActionNewFresh             Action = "new_fresh"
	ActionSettings             Action = "settings"
	ActionSettingsCategory     Action = "settings_cat"
	ActionOpenSetting          Action = "setting"
	ActionSetLanguage          Action = "set_language"
	ActionSetSessionView       Action = "set_view"
	ActionSetResumeSelection   Action = "set_resume"
	ActionSetIdleArchive       Action = "set_idle"
	ActionSetRetention         Action = "set_retention"
	ActionSetExpiry            Action = "set_expiry"
	ActionSetToolCalls         Action = "set_tools"
	ActionSetToolResults       Action = "set_results"
	ActionSetToolOutputLines   Action = "set_tool_lines"
	ActionSetThinking          Action = "set_thinking"
	ActionSetResponseCards     Action = "set_cards"
	ActionSetTerminalSnapshots Action = "set_terminal_shot"
	ActionSetNotifyFinished    Action = "set_bg_done"
	ActionSetNotifyError       Action = "set_bg_error"
	ActionSetNotifyAction      Action = "set_bg_action"
	ActionSetBgDismiss         Action = "set_bg_dismiss"
	ActionSetNodeSort          Action = "set_node_sort"
	ActionSetQuotaPoll         Action = "set_quota_poll"
	ActionSetVoiceBackend      Action = "set_voice"
	ActionSetOfflineQueue      Action = "set_offline_q"
	ActionConfirmVoiceEnable   Action = "voice_enable_yes"
	ActionCancelVoiceEnable    Action = "voice_enable_no"
	ActionNodeSpeechSetup      Action = "node_speech"
	ActionNodeSpeechBack       Action = "node_speech_back"
	ActionSelectNode           Action = "node"
	ActionSelectSession        Action = "session"
	ActionSelectArchive        Action = "archive_item"
	ActionSelectArchiveNode    Action = "archive_node"
	ActionArchivePrevious      Action = "archive_prev"
	ActionArchiveNext          Action = "archive_next"
	ActionArchiveBack          Action = "archive_back"
	ActionArchiveHistory       Action = "archive_history"
	ActionHistoryPrevious      Action = "history_prev"
	ActionHistoryNext          Action = "history_next"
	ActionPagePrevious         Action = "page_prev"
	ActionPageLatest           Action = "page_latest"
	ActionPageNext             Action = "page_next"
	ActionStop                 Action = "stop"
	ActionClose                Action = "close"
	ActionClear                Action = "clear"
	ActionRestore              Action = "restore"
	ActionTerminal             Action = "terminal"
	ActionConfirmClose         Action = "confirm_close"
	ActionConfirmClear         Action = "confirm_clear"
	ActionCancelControl        Action = "cancel_control"
	ActionKeyUp                Action = "key_up"
	ActionKeyDown              Action = "key_down"
	ActionKeyLeft              Action = "key_left"
	ActionKeyRight             Action = "key_right"
	ActionKeyEnter             Action = "key_enter"
	ActionKeyEscape            Action = "key_esc"
	ActionKeySpace             Action = "key_space"
	ActionKeyTab               Action = "key_tab"
	ActionKeyCtrlC             Action = "key_ctrlc"
	ActionKeyBack              Action = "key_back"
)

type Callback struct {
	Action Action
	Token  OpaqueToken
}

func (c Callback) Encode() (string, error) {
	action := string(c.Action)
	if !knownAction(c.Action) || !validPart(action, 20) {
		return "", fmt.Errorf("invalid callback action")
	}
	value := action
	if c.Token != "" {
		token := string(c.Token)
		if !validPart(token, 40) {
			return "", fmt.Errorf("invalid opaque callback token")
		}
		value += ":" + token
	}
	if len([]byte(value)) > MaxCallbackBytes {
		return "", fmt.Errorf("callback exceeds %d bytes", MaxCallbackBytes)
	}
	return value, nil
}

func DecodeCallback(value string) (Callback, error) {
	if value == "" || len([]byte(value)) > MaxCallbackBytes {
		return Callback{}, fmt.Errorf("invalid callback length")
	}
	actionValue, tokenValue, hasToken := strings.Cut(value, ":")
	callback := Callback{Action: Action(actionValue)}
	if hasToken {
		callback.Token = OpaqueToken(tokenValue)
	}
	if encoded, err := callback.Encode(); err != nil || encoded != value {
		return Callback{}, fmt.Errorf("invalid callback data")
	}
	return callback, nil
}

func knownAction(action Action) bool {
	switch action {
	case ActionNoop, ActionMenu, ActionSessions, ActionNodesPrevious, ActionNodesNext,
		ActionSessionsPrevious, ActionSessionsNext, ActionArchive, ActionStatus,
		ActionStatusRefresh, ActionStatusMode, ActionStatusLeaderNode,
		ActionStatusSettingsNode, ActionNodeSettings, ActionConfirmLeader,
		ActionClusterAdd, ActionClusterInvite, ActionClusterContract,
		ActionEnrollmentOpen, ActionEnrollmentApprove, ActionEnrollmentReject,
		ActionNodeDisable, ActionNodeDisableYes, ActionNodeEnable,
		ActionNodeDelete, ActionNodeDeleteYes,
		ActionNodeRename, ActionProviderAlias, ActionProviderAuth, ActionProviderAuthCancel,
		ActionBackendConnect, ActionBackendDisconnect,
		ActionNewSession, ActionNewNode, ActionNewBackend, ActionNewDirectory,
		ActionNewDirectoryUp, ActionNewDirectoryPick, ActionNewDirectoryBack,
		ActionNewDirectoryPrev, ActionNewDirectoryFirst,
		ActionNewDirectoryNext, ActionNewResume, ActionNewResumePrevious,
		ActionNewResumeFirst, ActionNewResumeNext, ActionNewFresh,
		ActionSettings, ActionSettingsCategory, ActionOpenSetting,
		ActionSetLanguage, ActionSetSessionView, ActionSetResumeSelection, ActionSetIdleArchive,
		ActionSetRetention, ActionSetExpiry, ActionSetToolCalls, ActionSetToolResults,
		ActionSetToolOutputLines, ActionSetThinking, ActionSetResponseCards, ActionSetTerminalSnapshots,
		ActionSelectNode, ActionSelectSession,
		ActionSetNotifyFinished, ActionSetNotifyError, ActionSetNotifyAction, ActionSetBgDismiss,
		ActionSetNodeSort, ActionSetQuotaPoll, ActionSetOfflineQueue,
		ActionSetVoiceBackend, ActionConfirmVoiceEnable, ActionCancelVoiceEnable,
		ActionNodeSpeechSetup, ActionNodeSpeechBack,
		ActionSelectArchive, ActionSelectArchiveNode, ActionArchivePrevious, ActionArchiveNext,
		ActionArchiveBack, ActionArchiveHistory, ActionHistoryPrevious, ActionHistoryNext,
		ActionPagePrevious, ActionPageLatest, ActionPageNext, ActionStop,
		ActionClose, ActionClear, ActionRestore, ActionTerminal, ActionConfirmClose, ActionConfirmClear,
		ActionCancelControl, ActionKeyUp, ActionKeyDown, ActionKeyLeft, ActionKeyRight,
		ActionKeyEnter, ActionKeyEscape, ActionKeySpace, ActionKeyTab, ActionKeyCtrlC,
		ActionKeyBack:
		return true
	default:
		return false
	}
}

func validPart(value string, maxBytes int) bool {
	if value == "" || len([]byte(value)) > maxBytes {
		return false
	}
	return strings.IndexFunc(value, func(char rune) bool {
		return !(char >= 'a' && char <= 'z') &&
			!(char >= 'A' && char <= 'Z') &&
			!(char >= '0' && char <= '9') &&
			char != '_' && char != '-'
	}) == -1
}

func button(label string, action Action, token OpaqueToken) Button {
	return Button{Label: label, Callback: Callback{Action: action, Token: token}}
}
