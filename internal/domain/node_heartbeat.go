package domain

import (
	"fmt"
	"strings"
	"time"
)

// PublishNodeHeartbeat records evidence observed by the current leader. The
// state transition is intentionally kept in the deterministic domain layer so
// runtime inventory, platform identity, and boot observation commit atomically.
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
