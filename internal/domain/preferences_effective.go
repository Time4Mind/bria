package domain

import (
	"fmt"
	"strings"
)

func (p UserPreferences) EffectiveVoiceBackend() VoiceBackend {
	if p.VoiceBackend == "" {
		return VoiceOff
	}
	return p.VoiceBackend
}

func (p UserPreferences) EffectiveTerminalSnapshots() TerminalSnapshotMode {
	if p.TerminalSnapshots == "" {
		return TerminalSnapshotWorking
	}
	return p.TerminalSnapshots
}

func (p UserPreferences) EffectiveNodeSort() NodeSortMode {
	if p.NodeSort == "" {
		return NodeSortCreated
	}
	return p.NodeSort
}

func (p UserPreferences) EffectiveQuotaPollMinutes() int {
	if p.QuotaPollMinutes == 0 {
		return 10
	}
	return p.QuotaPollMinutes
}

func (p UserPreferences) EffectiveToolOutputLines() int {
	if p.ToolOutputLines == 0 {
		return 15
	}
	return p.ToolOutputLines
}

func (p UserPreferences) EffectiveBackgroundDismissSwitches() int {
	if p.BackgroundDismissSwitches == 0 {
		return 1
	}
	return p.BackgroundDismissSwitches
}

func (p UserPreferences) SendsBackgroundNotification(kind BackgroundNoticeKind) bool {
	if kind == BackgroundWorking {
		return false
	}
	for _, muted := range p.MutedBackgroundNotifications {
		if muted == kind {
			return false
		}
	}
	return true
}

func (p *UserPreferences) SetBackgroundNotification(kind BackgroundNoticeKind, enabled bool) error {
	if kind == BackgroundWorking || !validBackgroundNoticeKind(kind) {
		return fmt.Errorf("unsupported background notification: %q", kind)
	}
	muted := make([]BackgroundNoticeKind, 0, len(p.MutedBackgroundNotifications)+1)
	for _, current := range p.MutedBackgroundNotifications {
		if current != kind {
			muted = append(muted, current)
		}
	}
	if !enabled {
		muted = append(muted, kind)
	}
	p.MutedBackgroundNotifications = canonicalMutedBackgroundNotifications(muted)
	return nil
}

func canonicalMutedBackgroundNotifications(kinds []BackgroundNoticeKind) []BackgroundNoticeKind {
	muted := make(map[BackgroundNoticeKind]bool, len(kinds))
	for _, kind := range kinds {
		muted[kind] = true
	}
	result := make([]BackgroundNoticeKind, 0, len(kinds))
	for _, kind := range []BackgroundNoticeKind{
		BackgroundFinished, BackgroundError, BackgroundNeedsAction,
	} {
		if muted[kind] {
			result = append(result, kind)
		}
	}
	return result
}

func (p UserPreferences) EffectiveResponseCards() ResponseCardMode {
	if p.ResponseCards == "" {
		return ResponseCardsKeepPaginated
	}
	return p.ResponseCards
}

func (p UserPreferences) ShowsCardEvent(eventType CardEventType) bool {
	for _, hidden := range p.HiddenCardEvents {
		if hidden == eventType {
			return false
		}
	}
	return true
}

func (p *UserPreferences) SetCardEventVisibility(eventType CardEventType, visible bool) error {
	if !validCardEventType(eventType) {
		return fmt.Errorf("unsupported card event: %q", eventType)
	}
	hidden := make([]CardEventType, 0, len(p.HiddenCardEvents)+1)
	for _, current := range p.HiddenCardEvents {
		if current != eventType {
			hidden = append(hidden, current)
		}
	}
	if !visible {
		hidden = append(hidden, eventType)
	}
	p.HiddenCardEvents = canonicalHiddenCardEvents(hidden)
	return nil
}

func canonicalHiddenCardEvents(events []CardEventType) []CardEventType {
	hidden := make(map[CardEventType]bool, len(events))
	for _, eventType := range events {
		hidden[eventType] = true
	}
	ordered := make([]CardEventType, 0, len(events))
	for _, eventType := range []CardEventType{
		CardEventToolCall, CardEventToolResult, CardEventThinking,
	} {
		if hidden[eventType] {
			ordered = append(ordered, eventType)
		}
	}
	return ordered
}

func (p UserPreferences) ShowsAllTechnicalCardEvents() bool {
	return p.ShowsCardEvent(CardEventToolCall) &&
		p.ShowsCardEvent(CardEventToolResult) &&
		p.ShowsCardEvent(CardEventThinking)
}

func validCardEventType(eventType CardEventType) bool {
	switch eventType {
	case CardEventToolCall, CardEventToolResult, CardEventThinking:
		return true
	default:
		return false
	}
}

func (p UserPreferences) EffectiveLanguage() Language {
	if p.Language == LanguageAuto {
		return LanguageEnglish
	}
	return p.Language
}

func (p UserPreferences) clone() UserPreferences {
	p.HiddenCardEvents = append([]CardEventType(nil), p.HiddenCardEvents...)
	p.MutedBackgroundNotifications = append(
		[]BackgroundNoticeKind(nil), p.MutedBackgroundNotifications...,
	)
	return p
}

func LanguageFromTelegram(code string) Language {
	code = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(code)), "_", "-")
	switch {
	case code == "ru" || strings.HasPrefix(code, "ru-"):
		return LanguageRussian
	case code == "zh" || strings.HasPrefix(code, "zh-"):
		return LanguageChinese
	default:
		return LanguageEnglish
	}
}
