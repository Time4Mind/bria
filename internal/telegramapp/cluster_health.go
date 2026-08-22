package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) SetClusterAgentWorkdir(path string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return errors.New("cluster agent workdir must be absolute")
	}
	h.clusterAgentWorkdir = path
	return nil
}

func isClusterHealthAction(action telegramui.Action) bool {
	return action == telegramui.ActionClusterHealth ||
		action == telegramui.ActionClusterHealthRefresh ||
		action == telegramui.ActionClusterHealthAgent
}

func (h *Handler) openClusterHealth(
	actor application.Principal,
) (telegramui.Screen, application.ClusterHealthReport, error) {
	report, err := h.service.ClusterHealth(actor, time.Now())
	if err != nil {
		return telegramui.Screen{}, application.ClusterHealthReport{}, err
	}
	h.cardDataMu.RLock()
	entries := len(h.cardTranscripts)
	evictions := h.cardEvictions
	reads, slow := h.transcriptReads, h.transcriptSlow
	total, maximum := h.transcriptTotal, h.transcriptMax
	h.cardDataMu.RUnlock()
	average := time.Duration(0)
	if reads > 0 {
		average = total / time.Duration(reads)
	}
	if evictions > 0 {
		report.Findings = append(report.Findings, application.ClusterHealthFinding{
			Code: "telegram.card_cache", Severity: application.HealthInfo,
			Evidence: fmt.Sprintf("entries=%d limit=%d evictions=%d", entries, maxCachedCardSessions, evictions),
			Remedy:   "inspect cache churn only if render latency rises; eviction itself is bounded and safe",
		})
	}
	if slow > 0 || maximum >= 2*time.Second {
		report.Findings = append(report.Findings, application.ClusterHealthFinding{
			Code: "telegram.transcript_slow", Severity: application.HealthWarning,
			Evidence: fmt.Sprintf("reads=%d timeouts=%d average=%s max=%s", reads, slow,
				average.Round(time.Millisecond), maximum.Round(time.Millisecond)),
			Remedy: "inspect the affected node-control transcript path; other sessions remain isolated",
		})
	}
	if h.activity != nil {
		if remaining, limited := h.activity.EditFloodWait(int64(actor.UserID)); limited {
			report.Findings = append(report.Findings, application.ClusterHealthFinding{
				Code: "telegram.edit_limited", Severity: application.HealthWarning,
				Evidence: fmt.Sprintf("local edit cooldown remaining=%s", remaining.Round(time.Second)),
				Remedy:   "wait for Telegram RetryAfter; avoid recreating the carrier message",
			})
		}
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		return healthSeverityRank(report.Findings[i].Severity) > healthSeverityRank(report.Findings[j].Severity)
	})
	target, targetErr := h.service.ClusterAgentTarget(actor)
	workdir := h.clusterAgentWorkdir
	if targetErr == nil {
		if sessionWorkdir, ok := h.service.ClusterAgentWorkdir(actor, target.ID); ok {
			workdir = sessionWorkdir
		}
	}
	input := telegramui.ClusterHealthInput{
		Copy: h.copy(actor), Leader: string(report.LeaderID), Online: report.Online, Enabled: report.Enabled,
		CacheEntries: entries, CacheLimit: maxCachedCardSessions, CacheEvictions: evictions,
		TranscriptAverage: average.Round(time.Millisecond).String(),
		TranscriptMaximum: maximum.Round(time.Millisecond).String(), TranscriptTimeouts: slow,
		AgentAvailable: targetErr == nil && h.starter != nil && h.controls.input != nil && workdir != "",
	}
	for _, finding := range report.Findings {
		input.Findings = append(input.Findings, telegramui.ClusterHealthFinding{
			Severity: string(finding.Severity), Title: h.healthFindingTitle(actor, finding),
			Evidence: finding.Evidence,
		})
	}
	return telegramui.RenderClusterHealth(input), report, nil
}

func (h *Handler) startClusterHealthAgent(
	ctx context.Context,
	actor application.Principal,
) (telegramui.Screen, domain.SessionRef, error) {
	if h.starter == nil || h.controls.input == nil || h.clusterAgentWorkdir == "" {
		return telegramui.Screen{}, domain.SessionRef{}, domain.ErrInvalidState
	}
	node, err := h.service.ClusterAgentTarget(actor)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	workdir := h.clusterAgentWorkdir
	if sessionWorkdir, ok := h.service.ClusterAgentWorkdir(actor, node.ID); ok {
		workdir = sessionWorkdir
	}
	_, report, err := h.openClusterHealth(actor)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	session, err := h.starter.Create(ctx, actor, application.CreateSessionRequest{
		NodeID: node.ID, Backend: "codex", Workdir: workdir,
	})
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	prompt := clusterHealthAgentPrompt(report)
	operationID := "cluster-health-agent-" + string(session.ID)
	if _, err := h.controls.input.SendInput(ctx, actor, operationID, prompt); err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	screen, err := h.renderSessionCard(ctx, actor, session.Ref(), 0)
	return screen, session.Ref(), err
}

func clusterHealthAgentPrompt(report application.ClusterHealthReport) string {
	return "Diagnose and safely repair this Bria cluster. Start by verifying the supplied evidence " +
		"against current logs and runtime state. Preserve quorum, user sessions and local changes; " +
		"do not perform destructive actions without explicit approval. After a fix, run proportional " +
		"tests and verify physical runtime health.\n\n" + report.CompactContext()
}

func (h *Handler) healthFindingTitle(
	actor application.Principal, finding application.ClusterHealthFinding,
) string {
	keys := map[string]i18n.Key{
		"raft.no_leader":           i18n.ClusterHealthNoLeader,
		"node.heartbeat_slow":      i18n.ClusterHealthHeartbeatSlow,
		"node.isolation_unready":   i18n.ClusterHealthIsolation,
		"node.offline":             i18n.ClusterHealthOffline,
		"node.version_drift":       i18n.ClusterHealthVersionDrift,
		"update.failed":            i18n.ClusterHealthUpdateFailed,
		"sessions.deferred":        i18n.ClusterHealthDeferred,
		"raft.apply_lag":           i18n.ClusterHealthRaftLag,
		"telegram.card_cache":      i18n.ClusterHealthCache,
		"telegram.transcript_slow": i18n.ClusterHealthTranscriptSlow,
		"telegram.edit_limited":    i18n.ClusterHealthTelegramLimited,
	}
	if key, ok := keys[finding.Code]; ok {
		return h.copy(actor).Text(key)
	}
	return finding.Title
}

func healthSeverityRank(severity application.HealthSeverity) int {
	switch severity {
	case application.HealthCritical:
		return 3
	case application.HealthWarning:
		return 2
	default:
		return 1
	}
}
