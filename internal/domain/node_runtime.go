package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// PublishNodeHeartbeat records evidence observed by the current leader. The
// state transition is intentionally kept in the deterministic domain layer so
// the runtime inventory and boot observation are committed atomically by Raft.
func (s *State) PublishNodeHeartbeat(
	nodeID NodeID,
	bootID string,
	version string,
	osName string,
	arch string,
	certificateFingerprint string,
	previousCertificateFingerprint string,
	backends []BackendDescriptor,
	archiveIDs []string,
	interactive []InteractivePromptReport,
	finals []TranscriptFinalReport,
	at time.Time,
) (BootRecoveryPlan, error) {
	if strings.TrimSpace(bootID) == "" {
		return BootRecoveryPlan{}, fmt.Errorf("boot id is required")
	}
	if err := validateInteractiveReports(nodeID, interactive); err != nil {
		return BootRecoveryPlan{}, err
	}
	if err := validateTranscriptFinalReports(nodeID, finals, at); err != nil {
		return BootRecoveryPlan{}, err
	}
	if err := s.observeNodeCertificate(
		nodeID, certificateFingerprint, previousCertificateFingerprint,
	); err != nil {
		return BootRecoveryPlan{}, err
	}
	if err := s.UpdateNodeInventory(nodeID, NodeOnline, version, backends, at); err != nil {
		return BootRecoveryPlan{}, err
	}
	if strings.TrimSpace(osName) != "" || strings.TrimSpace(arch) != "" {
		node := s.Nodes[nodeID]
		node.OS = strings.ToLower(strings.TrimSpace(osName))
		node.Arch = strings.ToLower(strings.TrimSpace(arch))
		if node.OS == "" || node.Arch == "" {
			return BootRecoveryPlan{}, fmt.Errorf("node platform must be complete")
		}
		s.Nodes[nodeID] = node
	}
	plan, err := s.ObserveNodeBoot(nodeID, bootID, at)
	if err != nil {
		return BootRecoveryPlan{}, err
	}
	if err := s.ObserveArchiveInventory(nodeID, archiveIDs, at); err != nil {
		return BootRecoveryPlan{}, err
	}
	if err := s.observeInteractivePrompts(nodeID, interactive, at); err != nil {
		return BootRecoveryPlan{}, err
	}
	if err := s.observeTranscriptFinals(nodeID, finals, at); err != nil {
		return BootRecoveryPlan{}, err
	}
	return plan, nil
}

// UpdateNodeInventory records what is installed without implicitly granting
// Bria permission to use it. Older snapshots are migrated to an explicitly
// empty selection on their first heartbeat.
func (s *State) UpdateNodeInventory(
	nodeID NodeID,
	status NodeStatus,
	version string,
	installed []BackendDescriptor,
	at time.Time,
) error {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	normalized, err := normalizeBackendDescriptors(installed)
	if err != nil {
		return err
	}
	connected := make(map[string]bool, len(node.Backends))
	if node.BackendSelectionInitialized {
		for _, backend := range node.Backends {
			connected[strings.ToLower(backend.Name)] = true
		}
	}
	node.InstalledBackends = normalized
	node.Backends = nil
	for _, backend := range normalized {
		if connected[backend.Name] {
			node.Backends = append(node.Backends, backend)
		}
	}
	node.BackendSelectionInitialized = true
	s.Nodes[nodeID] = node
	connectedNow := make(map[string]bool, len(node.Backends))
	for _, backend := range node.Backends {
		connectedNow[backend.Name] = true
	}
	for key, quota := range s.Quotas {
		if quota.NodeID == nodeID && !connectedNow[strings.ToLower(quota.Backend)] {
			delete(s.Quotas, key)
		}
	}
	return s.UpdateNodeRuntime(nodeID, status, version, node.Backends, at)
}

func (s *State) SetNodeBackendConnected(nodeID NodeID, backend string, connected bool) error {
	node, ok := s.Nodes[nodeID]
	name := strings.ToLower(strings.TrimSpace(backend))
	if !ok || name == "" {
		return ErrNotFound
	}
	var installed BackendDescriptor
	found := false
	for _, candidate := range node.InstalledBackends {
		if strings.EqualFold(candidate.Name, name) {
			installed, found = candidate, true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	result := make([]BackendDescriptor, 0, len(node.Backends)+1)
	already := false
	for _, candidate := range node.Backends {
		if strings.EqualFold(candidate.Name, name) {
			already = true
			if connected {
				result = append(result, installed)
			}
			continue
		}
		result = append(result, candidate)
	}
	if connected && !already {
		result = append(result, installed)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	node.Backends = result
	node.BackendSelectionInitialized = true
	s.Nodes[nodeID] = node
	if !connected {
		delete(s.Quotas, QuotaSnapshot{NodeID: nodeID, Backend: name}.Key())
		delete(s.ProviderAccountAliases, ProviderAccountAliasKey(nodeID, name))
	}
	return nil
}

func (s *State) observeNodeCertificate(nodeID NodeID, current string, previous string) error {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	if current == "" {
		return nil
	}
	if !validFingerprint(current) || (previous != "" && !validFingerprint(previous)) {
		return fmt.Errorf("%w: invalid node certificate fingerprint", ErrInvalidState)
	}
	if node.Fingerprint != "" && current != node.Fingerprint && previous != node.Fingerprint {
		return fmt.Errorf("%w: certificate rotation does not match active identity", ErrAccessDenied)
	}
	if node.Fingerprint != current {
		node.Fingerprint = current
		s.Nodes[nodeID] = node
	}
	return nil
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func (s *State) observeTranscriptFinals(
	nodeID NodeID,
	reports []TranscriptFinalReport,
	at time.Time,
) error {
	for _, report := range reports {
		ref := SessionRef{NodeID: nodeID, SessionID: report.SessionID}
		session, exists := s.Sessions[ref.Key()]
		if !exists || !session.IsLive() || session.RuntimePhase != RuntimeRunning ||
			session.RuntimeGeneration != report.Generation || report.Timestamp.Before(session.LastEventAt) {
			continue
		}
		if session.Revision == math.MaxUint64 {
			return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
		}
		previous := session.RuntimePhase
		session.RuntimePhase = RuntimeIdle
		session.InteractivePrompt = nil
		session.LastEventAt = report.Timestamp
		session.Revision++
		s.Sessions[ref.Key()] = session
		s.publishBackgroundTransition(previous, session, nil, at)
	}
	return nil
}

func validateTranscriptFinalReports(
	nodeID NodeID,
	reports []TranscriptFinalReport,
	at time.Time,
) error {
	if len(reports) > 512 {
		return fmt.Errorf("%w: transcript final inventory is too large", ErrInvalidState)
	}
	seen := make(map[string]bool, len(reports))
	for _, report := range reports {
		ref := SessionRef{NodeID: nodeID, SessionID: report.SessionID}
		if err := ref.Validate(); err != nil || report.Generation == 0 ||
			report.Timestamp.IsZero() || report.Timestamp.After(at.Add(5*time.Minute)) ||
			len(report.Digest) != 64 {
			return fmt.Errorf("%w: invalid transcript final report", ErrInvalidState)
		}
		for _, char := range report.Digest {
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
				return fmt.Errorf("%w: invalid transcript final digest", ErrInvalidState)
			}
		}
		if seen[ref.Key()] {
			return fmt.Errorf("duplicate transcript final for session %q", report.SessionID)
		}
		seen[ref.Key()] = true
	}
	return nil
}

func (s *State) observeInteractivePrompts(
	nodeID NodeID,
	reports []InteractivePromptReport,
	at time.Time,
) error {
	seen := make(map[string]InteractivePromptReport, len(reports))
	for _, report := range reports {
		key := SessionRef{NodeID: nodeID, SessionID: report.SessionID}.Key()
		seen[key] = report
	}
	for key, report := range seen {
		session, exists := s.Sessions[key]
		if !exists || session.NodeID != nodeID || !session.IsLive() ||
			report.Generation != session.RuntimeGeneration {
			continue
		}
		if report.Present {
			if session.InteractivePrompt != nil && session.InteractivePrompt.Hash == report.Hash &&
				session.InteractivePrompt.Kind == report.Kind &&
				session.RuntimePhase == RuntimeWaitingInput {
				continue
			}
			if session.Revision == math.MaxUint64 {
				return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
			}
			session.InteractivePrompt = &InteractivePrompt{
				Kind: report.Kind, Hash: report.Hash, DetectedAt: at,
			}
			session.RuntimePhase = RuntimeWaitingInput
			session.LastEventAt = at
			session.Revision++
			s.Sessions[key] = session
			s.publishBackgroundNotice(session, BackgroundNeedsAction, at)
			continue
		}
		if !report.Present && session.InteractivePrompt != nil {
			if session.Revision == math.MaxUint64 {
				return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
			}
			session.InteractivePrompt = nil
			if session.RuntimePhase == RuntimeWaitingInput {
				session.RuntimePhase = RuntimeRunning
			}
			session.LastEventAt = at
			session.Revision++
			s.Sessions[key] = session
			s.publishBackgroundNotice(session, BackgroundWorking, at)
		}
	}
	return nil
}

func validateInteractiveReports(nodeID NodeID, reports []InteractivePromptReport) error {
	seen := make(map[string]bool, len(reports))
	for _, report := range reports {
		if err := report.validate(); err != nil {
			return err
		}
		key := SessionRef{NodeID: nodeID, SessionID: report.SessionID}.Key()
		if seen[key] {
			return fmt.Errorf("duplicate interactive prompt for session %q", report.SessionID)
		}
		seen[key] = true
	}
	return nil
}

// MarkNodeOffline changes reachability without forging a new last-seen time.
// observedLastSeenAt is a compare-and-swap guard: a delayed timeout command
// cannot overwrite a heartbeat that was committed after the monitor snapshot.
func (s *State) MarkNodeOffline(nodeID NodeID, observedLastSeenAt time.Time) error {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	if node.LastSeenAt != observedLastSeenAt {
		return ErrStaleOperation
	}
	if node.Status == NodeOffline {
		return nil
	}
	node.Status = NodeOffline
	s.Nodes[nodeID] = node
	// Reachability is transient. Keep the user's selected node/session so the
	// same card can become read-only and recover in place when the node returns.
	// Destructive lifecycle transitions still repair navigation themselves.
	s.clearBackgroundNode(nodeID)
	return nil
}

func (s *State) UpdateNodeRuntime(
	nodeID NodeID,
	status NodeStatus,
	version string,
	backends []BackendDescriptor,
	at time.Time,
) error {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	if !node.Enabled() && status != NodeOffline {
		return fmt.Errorf("%w: node is disabled", ErrInvalidState)
	}
	if status != NodeOnline && status != NodeReconnecting && status != NodeOffline {
		return fmt.Errorf("unsupported node status: %q", status)
	}
	normalized, err := normalizeBackendDescriptors(backends)
	if err != nil {
		return err
	}
	node.Status = status
	node.Version = strings.TrimSpace(version)
	node.Backends = normalized
	node.LastSeenAt = at
	s.Nodes[nodeID] = node
	return nil
}

func normalizeBackendDescriptors(backends []BackendDescriptor) ([]BackendDescriptor, error) {
	normalized := cloneBackendDescriptors(backends)
	seen := make(map[string]bool, len(normalized))
	for index, backend := range normalized {
		name := strings.ToLower(strings.TrimSpace(backend.Name))
		if name == "" || seen[name] {
			return nil, fmt.Errorf("backend names must be non-empty and unique")
		}
		seen[name] = true
		normalized[index].Name = name
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Name < normalized[j].Name })
	return normalized, nil
}

func cloneBackendDescriptors(backends []BackendDescriptor) []BackendDescriptor {
	result := append([]BackendDescriptor(nil), backends...)
	for index := range result {
		result[index].Capabilities = append([]string(nil), result[index].Capabilities...)
	}
	return result
}
