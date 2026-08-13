package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const maxReportedArchives = 4096

// ObserveArchiveInventory makes archive finalization survive leader changes.
// A node reports only artifacts that were atomically committed and whose
// runtime was successfully deactivated.
func (s *State) ObserveArchiveInventory(
	nodeID NodeID,
	archiveIDs []string,
	at time.Time,
) error {
	if _, ok := s.Nodes[nodeID]; !ok {
		return ErrNotFound
	}
	if len(archiveIDs) > maxReportedArchives {
		return fmt.Errorf("%w: archive inventory is too large", ErrInvalidState)
	}
	reported := make(map[string]bool, len(archiveIDs))
	for _, archiveID := range archiveIDs {
		if strings.TrimSpace(archiveID) == "" || len(archiveID) > 128 ||
			strings.ContainsAny(archiveID, "/\\") || reported[archiveID] {
			return fmt.Errorf("%w: archive inventory is invalid", ErrInvalidState)
		}
		for _, character := range archiveID {
			if character <= 0x20 {
				return fmt.Errorf("%w: archive inventory is invalid", ErrInvalidState)
			}
		}
		reported[archiveID] = true
	}
	for key, session := range s.Sessions {
		if session.NodeID != nodeID || session.State != SessionArchived ||
			session.ArchiveReason == "" || session.ArchiveReady ||
			!reported[session.ArchiveID] {
			continue
		}
		if session.Revision == math.MaxUint64 {
			return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
		}
		session.ArchiveReady = true
		session.LastEventAt = at
		session.Revision++
		s.Sessions[key] = session
	}
	return nil
}
