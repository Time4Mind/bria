package domain

import (
	"cmp"
	"fmt"
	"strings"
	"time"
)

// MaxSessionTombstones bounds the replicated stale-replay guard and local
// sweeper worklist carried by a state snapshot.
const MaxSessionTombstones = 4096

// SessionTombstone is the bounded replicated hand-off from archive purge to
// the origin-node bundle sweeper. The session record and all replicated
// presentation state are removed first; the tombstone lets a later sweep
// retry bundle deletion after a crash or an offline origin node.
type SessionTombstone struct {
	Session           SessionRef `json:"session"`
	ArchiveID         string     `json:"archive_id"`
	RuntimeGeneration uint64     `json:"runtime_generation"`
	PurgedAt          time.Time  `json:"purged_at"`
}

// PurgeArchivedSession permanently removes a retention-expired archive
// record. It intentionally does not touch the workdir, .bria-inbox, or
// provider transcripts: those are origin-node/runtime data, not replicated
// archive state. A small tombstone prevents stale AddSession replay and
// identifies the local bundle that still needs cleanup.
func (s *State) PurgeArchivedSession(
	ref SessionRef,
	archiveID string,
	expectedRevision uint64,
	at time.Time,
) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	legacyUnready := archiveID == ""
	if !legacyUnready {
		if err := validateSessionArchiveID(archiveID); err != nil {
			return err
		}
	}
	if at.IsZero() {
		return fmt.Errorf("%w: purge timestamp is required", ErrInvalidState)
	}
	key := ref.Key()
	if tombstone, ok := s.SessionTombstones[key]; ok {
		if tombstone.Session == ref && tombstone.ArchiveID == archiveID {
			return nil
		}
		return fmt.Errorf("%w: session identity was purged with a different archive", ErrInvalidState)
	}
	session, ok := s.Sessions[key]
	if !ok {
		return ErrNotFound
	}
	if session.Ref() != ref || session.State != SessionArchived {
		return ErrInvalidState
	}
	if legacyUnready {
		if session.ArchiveReady || session.ArchiveID != "" {
			return ErrInvalidState
		}
	} else if !session.ArchiveReady || session.ArchiveID != archiveID {
		return ErrInvalidState
	}
	for otherKey, other := range s.Sessions {
		if archiveID != "" && otherKey != key && other.ArchiveID == archiveID {
			return fmt.Errorf("%w: archive id belongs to multiple sessions", ErrInvalidState)
		}
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}

	// Navigation repair runs before deleting the session, so the existing
	// deterministic same-node fallback logic remains the single source of
	// truth for every viewer.
	s.repairNavigationAfterClosed(ref)
	delete(s.Sessions, key)
	s.clearDeferredInputs(ref)
	s.clearBackgroundSession(ref)
	for grantKey, grant := range s.Grants {
		if grant.Session == ref {
			delete(s.Grants, grantKey)
		}
	}
	for userID, activity := range s.Navigation.SessionActivityByUser {
		delete(activity, key)
		if len(activity) == 0 {
			delete(s.Navigation.SessionActivityByUser, userID)
		}
	}
	for userID, views := range s.TelegramSessionViews {
		delete(views, key)
		if len(views) == 0 {
			delete(s.TelegramSessionViews, userID)
		}
	}
	for userID, card := range s.TelegramResponseCards {
		if card.Session != ref {
			continue
		}
		// Keep only the Telegram carrier identity. Rich media, pane/screen
		// hashes and session watermarks can otherwise replay stale content.
		s.TelegramResponseCards[userID] = TelegramResponseCard{
			ChatID: card.ChatID, MessageID: card.MessageID,
		}
	}
	if s.SessionTombstones == nil {
		s.SessionTombstones = make(map[string]SessionTombstone)
	}
	s.SessionTombstones[key] = SessionTombstone{
		Session: ref, ArchiveID: archiveID,
		RuntimeGeneration: session.RuntimeGeneration, PurgedAt: at.UTC(),
	}
	s.pruneSessionTombstones()
	return nil
}

// PurgeSession is the lifecycle-facing name used by replicated callers.
func (s *State) PurgeSession(
	ref SessionRef,
	archiveID string,
	expectedRevision uint64,
	at time.Time,
) error {
	return s.PurgeArchivedSession(ref, archiveID, expectedRevision, at)
}

func validateSessionArchiveID(archiveID string) error {
	if strings.TrimSpace(archiveID) == "" || archiveID == "." || archiveID == ".." ||
		len(archiveID) > 128 ||
		strings.ContainsAny(archiveID, "/\\") {
		return fmt.Errorf("%w: archive id is invalid", ErrInvalidState)
	}
	for _, character := range archiveID {
		if character <= 0x20 {
			return fmt.Errorf("%w: archive id is invalid", ErrInvalidState)
		}
	}
	return nil
}

// pruneSessionTombstones keeps the replicated stale-replay guard bounded.
// PurgedAt and the fully-qualified session key provide a total order, so map
// iteration order cannot change which tombstone survives a snapshot replay.
func (s *State) pruneSessionTombstones() {
	for len(s.SessionTombstones) > MaxSessionTombstones {
		oldestKey := ""
		for key, tombstone := range s.SessionTombstones {
			if oldestKey == "" {
				oldestKey = key
				continue
			}
			oldest := s.SessionTombstones[oldestKey]
			if tombstone.PurgedAt.Before(oldest.PurgedAt) ||
				(tombstone.PurgedAt.Equal(oldest.PurgedAt) && cmp.Compare(key, oldestKey) < 0) {
				oldestKey = key
			}
		}
		delete(s.SessionTombstones, oldestKey)
	}
}

func (s *State) cloneSessionTombstones(clone *State) {
	for key, tombstone := range s.SessionTombstones {
		clone.SessionTombstones[key] = tombstone
	}
}
