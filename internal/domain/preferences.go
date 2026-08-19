package domain

import (
	"fmt"
)

type SessionViewMode string

const (
	ViewHostFirst SessionViewMode = "host_first"
	ViewAllHosts  SessionViewMode = "all_hosts"
)

type Language string

const (
	LanguageAuto    Language = ""
	LanguageEnglish Language = "en"
	LanguageRussian Language = "ru"
	LanguageChinese Language = "zh"
)

type ArchiveExpiryAction string

type CardEventType string

type ResponseCardMode string
type NodeSortMode string
type TerminalSnapshotMode string
type VoiceBackend string

const (
	CardEventToolCall   CardEventType = "tool_call"
	CardEventToolResult CardEventType = "tool_result"
	CardEventThinking   CardEventType = "thinking"
)

const (
	NodeSortCreated NodeSortMode = "created"
	NodeSortName    NodeSortMode = "name"
	NodeSortLeader  NodeSortMode = "leader"
)

const (
	ResponseCardsKeepPaginated ResponseCardMode = "keep_paginated"
	ResponseCardsKeepLatest    ResponseCardMode = "keep_latest"
	ResponseCardsReplace       ResponseCardMode = "replace_paginated"
)

const (
	TerminalSnapshotWorking TerminalSnapshotMode = "working"
	TerminalSnapshotAlways  TerminalSnapshotMode = "always"
	TerminalSnapshotNever   TerminalSnapshotMode = "never"
)

const (
	VoiceAuto    VoiceBackend = "auto"
	VoiceWhisper VoiceBackend = "whisper"
	VoiceApple   VoiceBackend = "apple"
	VoiceOff     VoiceBackend = "off"
)

const (
	ArchiveRemoveRecord ArchiveExpiryAction = "remove_record"
	ArchiveRemoveAll    ArchiveExpiryAction = "remove_all"
)

type UserPreferences struct {
	Language                     Language               `json:"language,omitempty"`
	SessionView                  SessionViewMode        `json:"session_view"`
	IdleArchiveHours             int                    `json:"idle_archive_hours"`
	ArchiveRetentionDays         int                    `json:"archive_retention_days"`
	ArchiveExpiryAction          ArchiveExpiryAction    `json:"archive_expiry_action"`
	ResponseCards                ResponseCardMode       `json:"response_cards,omitempty"`
	HiddenCardEvents             []CardEventType        `json:"hidden_card_events,omitempty"`
	MutedBackgroundNotifications []BackgroundNoticeKind `json:"muted_background_notifications,omitempty"`
	BackgroundDismissSwitches    int                    `json:"background_dismiss_switches,omitempty"`
	NodeSort                     NodeSortMode           `json:"node_sort,omitempty"`
	QuotaPollMinutes             int                    `json:"quota_poll_minutes,omitempty"`
	ToolOutputLines              int                    `json:"tool_output_lines,omitempty"`
	TerminalSnapshots            TerminalSnapshotMode   `json:"terminal_snapshots,omitempty"`
	SkipResumeSelection          bool                   `json:"skip_resume_selection,omitempty"`
	VoiceBackend                 VoiceBackend           `json:"voice_backend,omitempty"`
	OfflineInputQueueLimit       int                    `json:"offline_input_queue_limit,omitempty"`
}

func DefaultUserPreferences() UserPreferences {
	return UserPreferences{
		SessionView:               ViewHostFirst,
		IdleArchiveHours:          6,
		ArchiveRetentionDays:      14,
		ArchiveExpiryAction:       ArchiveRemoveRecord,
		ResponseCards:             ResponseCardsKeepPaginated,
		BackgroundDismissSwitches: 1,
		NodeSort:                  NodeSortCreated,
		QuotaPollMinutes:          10,
		ToolOutputLines:           15,
		TerminalSnapshots:         TerminalSnapshotWorking,
		VoiceBackend:              VoiceOff,
		OfflineInputQueueLimit:    5,
	}
}

func (p UserPreferences) Validate() error {
	if p.Language != LanguageAuto && p.Language != LanguageEnglish &&
		p.Language != LanguageRussian && p.Language != LanguageChinese {
		return fmt.Errorf("unsupported language: %q", p.Language)
	}
	if p.SessionView != ViewHostFirst && p.SessionView != ViewAllHosts {
		return fmt.Errorf("unsupported session view: %q", p.SessionView)
	}
	if p.IdleArchiveHours != 0 && p.IdleArchiveHours != 6 &&
		p.IdleArchiveHours != 12 && p.IdleArchiveHours != 24 {
		return fmt.Errorf("idle archive hours must be 0, 6, 12, or 24")
	}
	if p.ArchiveRetentionDays != 0 && p.ArchiveRetentionDays != 14 &&
		p.ArchiveRetentionDays != 30 {
		return fmt.Errorf("archive retention days must be 0, 14, or 30")
	}
	if p.ArchiveExpiryAction != ArchiveRemoveRecord &&
		p.ArchiveExpiryAction != ArchiveRemoveAll {
		return fmt.Errorf("unsupported archive expiry action: %q", p.ArchiveExpiryAction)
	}
	if p.ResponseCards != "" &&
		p.ResponseCards != ResponseCardsKeepPaginated &&
		p.ResponseCards != ResponseCardsKeepLatest &&
		p.ResponseCards != ResponseCardsReplace {
		return fmt.Errorf("unsupported response card mode: %q", p.ResponseCards)
	}
	seen := make(map[CardEventType]bool, len(p.HiddenCardEvents))
	for _, eventType := range p.HiddenCardEvents {
		if !validCardEventType(eventType) || seen[eventType] {
			return fmt.Errorf("unsupported or duplicate hidden card event: %q", eventType)
		}
		seen[eventType] = true
	}
	if p.BackgroundDismissSwitches != 0 && p.BackgroundDismissSwitches != 1 &&
		p.BackgroundDismissSwitches != 3 && p.BackgroundDismissSwitches != 5 &&
		p.BackgroundDismissSwitches != 10 {
		return fmt.Errorf("background dismissal switches must be 1, 3, 5, or 10")
	}
	if p.NodeSort != "" && p.NodeSort != NodeSortCreated && p.NodeSort != NodeSortName &&
		p.NodeSort != NodeSortLeader {
		return fmt.Errorf("unsupported node sort: %q", p.NodeSort)
	}
	if p.QuotaPollMinutes != 0 && p.QuotaPollMinutes != 5 && p.QuotaPollMinutes != 10 {
		return fmt.Errorf("quota poll minutes must be 5 or 10")
	}
	if p.ToolOutputLines != 0 && (p.ToolOutputLines < 5 || p.ToolOutputLines > 30 || p.ToolOutputLines%5 != 0) {
		return fmt.Errorf("tool output lines must be 5 to 30 in steps of 5")
	}
	if p.TerminalSnapshots != "" && p.TerminalSnapshots != TerminalSnapshotWorking &&
		p.TerminalSnapshots != TerminalSnapshotAlways && p.TerminalSnapshots != TerminalSnapshotNever {
		return fmt.Errorf("unsupported terminal snapshot mode: %q", p.TerminalSnapshots)
	}
	if p.VoiceBackend != "" && p.VoiceBackend != VoiceAuto &&
		p.VoiceBackend != VoiceWhisper && p.VoiceBackend != VoiceApple &&
		p.VoiceBackend != VoiceOff {
		return fmt.Errorf("unsupported voice backend: %q", p.VoiceBackend)
	}
	if p.OfflineInputQueueLimit != 0 && p.OfflineInputQueueLimit != 5 &&
		p.OfflineInputQueueLimit != 10 && p.OfflineInputQueueLimit != 20 {
		return fmt.Errorf("offline input queue limit must be 5, 10, or 20")
	}
	muted := make(map[BackgroundNoticeKind]bool, len(p.MutedBackgroundNotifications))
	for _, kind := range p.MutedBackgroundNotifications {
		if kind == BackgroundWorking || !validBackgroundNoticeKind(kind) || muted[kind] {
			return fmt.Errorf("unsupported or duplicate muted background notification: %q", kind)
		}
		muted[kind] = true
	}
	return nil
}
