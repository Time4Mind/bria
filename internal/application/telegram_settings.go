package application

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (s *Service) Preferences(actor Principal) (domain.UserPreferences, error) {
	if actor.UserID <= 0 {
		return domain.UserPreferences{}, domain.ErrAccessDenied
	}
	state := s.reader.State()
	preferences, ok := state.Preferences[actor.UserID]
	if !ok {
		return domain.UserPreferences{}, domain.ErrAccessDenied
	}
	return preferences, nil
}

func (p *TelegramProjector) Settings(actor Principal) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	preferences, ok := state.Preferences[actor.UserID]
	if !ok {
		return telegramui.Screen{}, domain.ErrAccessDenied
	}
	return telegramui.RenderSettings(settingsInput(state, actor.UserID, preferences)), nil
}

func (p *TelegramProjector) SettingsCategory(
	actor Principal,
	category telegramui.SettingsCategory,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	preferences := state.Preferences[actor.UserID]
	input := settingsInput(state, actor.UserID, preferences)
	if category == telegramui.CategoryCluster {
		for _, request := range state.EnrollmentRequests {
			if request.Status != domain.EnrollmentPending {
				continue
			}
			token, tokenErr := p.tokens.Choice(
				actor.UserID, telegramui.ActionEnrollmentOpen, "enrollment", request.ID,
			)
			if tokenErr != nil {
				return telegramui.Screen{}, tokenErr
			}
			input.PendingEnrollments = append(input.PendingEnrollments,
				telegramui.PendingEnrollmentItem{Name: request.Name, Token: token})
		}
		sort.Slice(input.PendingEnrollments, func(i, j int) bool {
			return input.PendingEnrollments[i].Name < input.PendingEnrollments[j].Name
		})
	}
	return telegramui.RenderSettingsCategory(input, category)
}

func (p *TelegramProjector) Setting(
	actor Principal,
	setting telegramui.SettingID,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	preferences := state.Preferences[actor.UserID]
	input := settingsInput(state, actor.UserID, preferences)
	if setting == telegramui.SettingLeaderNode {
		for _, node := range visibleNodes(state, actor) {
			token, tokenErr := p.tokens.Node(
				actor.UserID, telegramui.ActionSetLeaderNode, node.ID,
			)
			if tokenErr != nil {
				return telegramui.Screen{}, tokenErr
			}
			input.LeaderNodes = append(input.LeaderNodes, telegramui.LeaderSettingNode{
				Name: node.Name, Selected: state.LeaderPolicy.NodeID == node.ID,
				Disabled: !node.Enabled() || node.Status == domain.NodeOffline, Token: token,
			})
		}
	}
	return telegramui.RenderSetting(input, setting)
}

func (p *TelegramProjector) preferences(actor Principal) (domain.UserPreferences, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return domain.UserPreferences{}, err
	}
	preferences, ok := state.Preferences[actor.UserID]
	if !ok {
		return domain.UserPreferences{}, domain.ErrAccessDenied
	}
	return preferences, nil
}

func settingsInput(
	state *domain.State,
	userID domain.UserID,
	preferences domain.UserPreferences,
) telegramui.SettingsInput {
	preferredLeader := ""
	if node, ok := state.Nodes[state.LeaderPolicy.NodeID]; ok {
		preferredLeader = node.Name
	}
	return telegramui.SettingsInput{
		Copy:              i18n.For(string(preferences.EffectiveLanguage())),
		AllHosts:          preferences.SessionView == domain.ViewAllHosts,
		ResumeSelection:   !preferences.SkipResumeSelection,
		ShowToolCalls:     preferences.ShowsCardEvent(domain.CardEventToolCall),
		ShowToolResults:   preferences.ShowsCardEvent(domain.CardEventToolResult),
		ToolOutputLines:   preferences.EffectiveToolOutputLines(),
		ShowThinking:      preferences.ShowsCardEvent(domain.CardEventThinking),
		ResponseCards:     string(preferences.EffectiveResponseCards()),
		TerminalSnapshots: string(preferences.EffectiveTerminalSnapshots()),
		IdleHours:         preferences.IdleArchiveHours,
		RetentionDays:     preferences.ArchiveRetentionDays,
		RemoveAllOnPurge:  preferences.ArchiveExpiryAction == domain.ArchiveRemoveAll,
		NotifyFinished:    preferences.SendsBackgroundNotification(domain.BackgroundFinished),
		NotifyError:       preferences.SendsBackgroundNotification(domain.BackgroundError),
		NotifyAction:      preferences.SendsBackgroundNotification(domain.BackgroundNeedsAction),
		BackgroundDismiss: preferences.EffectiveBackgroundDismissSwitches(),
		NodeSort:          string(preferences.EffectiveNodeSort()),
		QuotaPollMinutes:  preferences.EffectiveQuotaPollMinutes(),
		LeaderAutomatic:   state.LeaderPolicy.EffectiveMode() == domain.LeaderSelectionAutomatic,
		PreferredLeader:   preferredLeader,
		VoiceBackend:      string(preferences.EffectiveVoiceBackend()),
		OfflineQueueLimit: preferences.EffectiveOfflineInputQueueLimit(),
		ClusterAccounts:   clusterAccountSummary(state, userID),
	}
}

func clusterAccountSummary(state *domain.State, userID domain.UserID) string {
	type accountRow struct {
		text      string
		collected int64
		nodeID    domain.NodeID
	}
	rows := make(map[string]accountRow)
	for _, snapshot := range state.Quotas {
		if !state.CanAccessNode(userID, snapshot.NodeID) {
			continue
		}
		account := snapshot.AccountLabel
		if account == "" {
			account = snapshot.AccountID
		}
		if account == "" {
			account = state.ProviderAccountAlias(snapshot.NodeID, snapshot.Backend)
		}
		if account == "" {
			account = string(snapshot.NodeID)
		}
		usage := "—"
		if snapshot.Weekly != nil {
			usage = fmt.Sprintf("%d%%", snapshot.Weekly.UsedPercent)
		} else if snapshot.FiveHour != nil {
			usage = fmt.Sprintf("%d%%", snapshot.FiveHour.UsedPercent)
		}
		accountKey, _ := quotaAccountIdentity(state, snapshot)
		key := strings.ToLower(snapshot.Backend) + "\x00" + accountKey
		candidate := accountRow{
			text:      html.EscapeString(snapshot.Backend) + " · " + html.EscapeString(account) + " · " + usage,
			collected: snapshot.CollectedAt.UnixNano(), nodeID: snapshot.NodeID,
		}
		current, exists := rows[key]
		if !exists || candidate.collected > current.collected ||
			(candidate.collected == current.collected && candidate.nodeID < current.nodeID) {
			rows[key] = candidate
		}
	}
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, rows[key].text)
	}
	return strings.Join(result, "\n")
}
