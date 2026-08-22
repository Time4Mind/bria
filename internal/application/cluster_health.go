package application

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type HealthSeverity string

const (
	HealthInfo     HealthSeverity = "info"
	HealthWarning  HealthSeverity = "warning"
	HealthCritical HealthSeverity = "critical"
)

type ClusterHealthFinding struct {
	Code     string
	Severity HealthSeverity
	Title    string
	Evidence string
	Remedy   string
}

type ClusterHealthReport struct {
	GeneratedAt time.Time
	LeaderID    domain.NodeID
	Enabled     int
	Online      int
	Nodes       []domain.Node
	Findings    []ClusterHealthFinding
}

func (s *Service) ClusterHealth(
	actor Principal, now time.Time,
) (ClusterHealthReport, error) {
	if !s.IsOwner(actor) {
		return ClusterHealthReport{}, domain.ErrAccessDenied
	}
	state := s.reader.State()
	report := ClusterHealthReport{GeneratedAt: now.UTC()}
	if s.leaders != nil {
		report.LeaderID = domain.NodeID(s.leaders.LeaderID())
	}
	for _, node := range state.Nodes {
		if !node.Enabled() {
			continue
		}
		report.Enabled++
		if node.Status == domain.NodeOnline {
			report.Online++
		}
		report.Nodes = append(report.Nodes, node)
	}
	sort.Slice(report.Nodes, func(i, j int) bool {
		if report.Nodes[i].Name != report.Nodes[j].Name {
			return report.Nodes[i].Name < report.Nodes[j].Name
		}
		return report.Nodes[i].ID < report.Nodes[j].ID
	})
	report.Findings = append(report.Findings, clusterStateFindings(state, report, now)...)
	if stats, ok := s.leaders.(interface{ Stats() map[string]string }); ok {
		report.Findings = append(report.Findings, raftStatsFindings(stats.Stats())...)
	}
	return report, nil
}

func (s *Service) ClusterAgentTarget(actor Principal) (domain.Node, error) {
	if !s.IsOwner(actor) || s.leaders == nil {
		return domain.Node{}, domain.ErrAccessDenied
	}
	state := s.reader.State()
	leaderID := domain.NodeID(s.leaders.LeaderID())
	node, ok := state.Nodes[leaderID]
	if !ok || !node.Enabled() || node.Status != domain.NodeOnline ||
		!node.BackendExecutionAllowed() || !supportsBackend(node, "codex", createCapability) {
		return domain.Node{}, domain.ErrInvalidState
	}
	return node, nil
}

// ClusterAgentWorkdir reuses the most recently active Bria checkout already
// proven usable by one of this owner's sessions on the leader. The daemon's
// own working directory is commonly a data directory, not a source checkout.
func (s *Service) ClusterAgentWorkdir(
	actor Principal, nodeID domain.NodeID,
) (string, bool) {
	if !s.IsOwner(actor) {
		return "", false
	}
	var selected domain.Session
	for _, session := range s.reader.State().Sessions {
		if session.OwnerID != actor.UserID || session.NodeID != nodeID ||
			!session.IsLive() || strings.ToLower(filepath.Base(session.Workdir)) != "bria" {
			continue
		}
		if selected.ID == "" || session.LastEventAt.After(selected.LastEventAt) {
			selected = session
		}
	}
	return selected.Workdir, selected.ID != ""
}

func clusterStateFindings(
	state *domain.State, report ClusterHealthReport, now time.Time,
) []ClusterHealthFinding {
	result := make([]ClusterHealthFinding, 0)
	if report.LeaderID == "" {
		result = append(result, ClusterHealthFinding{
			Code: "raft.no_leader", Severity: HealthCritical, Title: "Raft leader is unavailable",
			Evidence: "the local cluster view has no elected leader",
			Remedy:   "check node connectivity and preserve quorum before restarting anything",
		})
	}
	versions := make(map[string]int)
	offline := make([]string, 0)
	for _, node := range report.Nodes {
		versions[node.Version]++
		if node.Status != domain.NodeOnline {
			offline = append(offline, node.Name)
			continue
		}
		if !node.LastSeenAt.IsZero() && now.Sub(node.LastSeenAt) > 12*time.Second {
			result = append(result, ClusterHealthFinding{
				Code: "node.heartbeat_slow", Severity: HealthWarning,
				Title:    "Heartbeat is delayed on " + node.Name,
				Evidence: fmt.Sprintf("last heartbeat was %s ago", now.Sub(node.LastSeenAt).Round(time.Second)),
				Remedy:   "inspect node-control latency, CPU pressure and the link to the leader",
			})
		}
		if node.BackendIsolationRequired && !node.BackendExecutionAllowed() {
			result = append(result, ClusterHealthFinding{
				Code: "node.isolation_unready", Severity: HealthCritical,
				Title:    "Backend isolation is not ready on " + node.Name,
				Evidence: "execution is blocked by the replicated isolation policy",
				Remedy:   "repair the isolated runner before starting or resuming sessions",
			})
		}
	}
	if len(offline) > 0 {
		result = append(result, ClusterHealthFinding{
			Code: "node.offline", Severity: HealthWarning, Title: "Some nodes are offline",
			Evidence: strings.Join(offline, ", "),
			Remedy:   "restore connectivity, then start a rollout if the reconnected node remains behind",
		})
	}
	if len(versions) > 1 {
		parts := make([]string, 0, len(versions))
		for version, count := range versions {
			parts = append(parts, fmt.Sprintf("%s: %d", version, count))
		}
		sort.Strings(parts)
		result = append(result, ClusterHealthFinding{
			Code: "node.version_drift", Severity: HealthWarning, Title: "Node versions differ",
			Evidence: strings.Join(parts, ", "), Remedy: "finish or retry the rolling cluster update",
		})
	}
	if state.ClusterUpdate != nil && state.ClusterUpdate.Phase == domain.ClusterUpdateFailed {
		result = append(result, ClusterHealthFinding{
			Code: "update.failed", Severity: HealthCritical, Title: "The last cluster update failed",
			Evidence: state.ClusterUpdate.Error, Remedy: "open update details and retry from the failed node",
		})
	}
	queued := 0
	for _, inputs := range state.DeferredInputs {
		queued += len(inputs)
	}
	if queued > 0 {
		result = append(result, ClusterHealthFinding{
			Code: "sessions.deferred", Severity: HealthInfo, Title: "Inputs are waiting for node recovery",
			Evidence: fmt.Sprintf("queued inputs: %d", queued),
			Remedy:   "restore the affected node; queued inputs preserve FIFO order",
		})
	}
	missingBindings := 0
	for _, session := range state.Sessions {
		if session.IsLive() && session.RuntimeIssue == domain.RuntimeIssueProviderHookUnavailable {
			missingBindings++
		}
	}
	if missingBindings > 0 {
		result = append(result, ClusterHealthFinding{
			Code: "sessions.provider_hook_unavailable", Severity: HealthCritical,
			Title:    "Provider lifecycle integration is unavailable",
			Evidence: fmt.Sprintf("live sessions without a provider binding: %d", missingBindings),
			Remedy:   "repair the stable provider hook and restart the affected sessions",
		})
	}
	return result
}

func raftStatsFindings(stats map[string]string) []ClusterHealthFinding {
	commit, commitErr := strconv.ParseUint(stats["commit_index"], 10, 64)
	applied, appliedErr := strconv.ParseUint(stats["applied_index"], 10, 64)
	if commitErr != nil || appliedErr != nil || commit <= applied+20 {
		return nil
	}
	return []ClusterHealthFinding{{
		Code: "raft.apply_lag", Severity: HealthWarning, Title: "Raft apply is behind commit",
		Evidence: fmt.Sprintf("commit=%d applied=%d lag=%d", commit, applied, commit-applied),
		Remedy:   "inspect disk latency and state-machine command duration on the leader",
	}}
}

func (report ClusterHealthReport) CompactContext() string {
	lines := []string{
		"Bria cluster health snapshot",
		fmt.Sprintf("generated_at=%s leader=%s nodes_online=%d/%d",
			report.GeneratedAt.Format(time.RFC3339), report.LeaderID, report.Online, report.Enabled),
	}
	for _, node := range report.Nodes {
		lines = append(lines, fmt.Sprintf("node=%s id=%s status=%s version=%s last_seen=%s platform=%s/%s",
			node.Name, node.ID, node.Status, node.Version, node.LastSeenAt.Format(time.RFC3339), node.OS, node.Arch))
	}
	for _, finding := range report.Findings {
		lines = append(lines, fmt.Sprintf("finding=%s severity=%s evidence=%s remedy=%s",
			finding.Code, finding.Severity, finding.Evidence, finding.Remedy))
	}
	return strings.Join(lines, "\n")
}
