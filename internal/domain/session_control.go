package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func (s *State) PublishSessionRuntime(
	ref SessionRef,
	generation uint64,
	phase RuntimePhase,
	result *SessionOperationResult,
	at time.Time,
) error {
	return s.PublishSessionRuntimeWithIssue(ref, generation, phase, result, "", at)
}

func (s *State) PublishSessionRuntimeWithIssue(
	ref SessionRef,
	generation uint64,
	phase RuntimePhase,
	result *SessionOperationResult,
	issue string,
	at time.Time,
) error {
	session, err := s.mutableLiveSession(ref)
	if err != nil {
		return err
	}
	if generation == 0 || generation != session.RuntimeGeneration {
		return ErrStaleOperation
	}
	if !validRuntimePhase(phase) {
		return fmt.Errorf("%w: unsupported runtime phase %q", ErrInvalidState, phase)
	}
	if issue != "" && (phase != RuntimeDegraded || issue != RuntimeIssueProviderHookUnavailable) {
		return fmt.Errorf("%w: unsupported runtime issue", ErrInvalidState)
	}
	previousPhase := session.RuntimePhase
	if result != nil {
		if err := validateOperationResult(*result); err != nil {
			return err
		}
		copyResult := *result
		copyResult.At = at
		session.LastOperation = &copyResult
		updateVoiceAcknowledgement(&session, copyResult, at)
	}
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	session.RuntimePhase = phase
	// A regular runtime result must not accidentally hide a previously
	// diagnosed lifecycle-integration failure. The issue is cleared only by a
	// real phase transition (for example, the late provider binding moves the
	// session back to idle) or replaced by an explicit issue report.
	if issue != "" || phase != RuntimeDegraded || session.RuntimeIssue == "" {
		session.RuntimeIssue = issue
	}
	if phase != RuntimeWaitingInput {
		session.InteractivePrompt = nil
	}
	if !isDeliveredInputAcknowledgement(previousPhase, phase, result) {
		session.LastEventAt = at
	}
	session.Revision++
	s.Sessions[ref.Key()] = session
	s.publishBackgroundTransition(previousPhase, session, result, at)
	return nil
}

func (s *State) ClearSession(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
	operationID string,
	at time.Time,
) error {
	if !s.CanPerformSessionAction(actorID, ref, ActionClear) {
		return ErrAccessDenied
	}
	session, err := s.mutableLiveSession(ref)
	if err != nil {
		return err
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" || len(operationID) > 128 {
		return fmt.Errorf("%w: operation id must contain 1 to 128 characters", ErrInvalidState)
	}
	if session.RuntimeGeneration == math.MaxUint64 || session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session counter exhausted", ErrInvalidState)
	}
	session.Name = ""
	session.NameFormatVersion = 0
	session.ArchiveDescription = nil
	session.DescriptionVersion = 0
	session.ProviderSessionID = ""
	session.ProviderResume = false
	session.ProviderBindingSince = at
	session.RuntimeGeneration++
	session.RuntimePhase = RuntimeIdle
	session.RuntimeIssue = ""
	session.InteractivePrompt = nil
	session.ResumePending = false
	session.LastEventAt = at
	session.Revision++
	session.LastOperation = &SessionOperationResult{
		OperationID: operationID,
		Action:      ActionClear,
		Status:      OperationSucceeded,
		At:          at,
	}
	session.VoiceAcknowledgements = nil
	session.UserRequestSeen = false
	session.UserRequestTracked = true
	s.Sessions[ref.Key()] = session
	s.clearDeferredInputs(ref)
	s.clearBackgroundSession(ref)
	return nil
}

const maxVoiceAcknowledgements = 16

func updateVoiceAcknowledgement(session *Session, result SessionOperationResult, at time.Time) {
	if result.InputKind != "voice" {
		return
	}
	if result.TranscriptOrdinal == 0 {
		result.TranscriptOrdinal = 1
	}
	for index := range session.VoiceAcknowledgements {
		ack := &session.VoiceAcknowledgements[index]
		if ack.OperationID != result.OperationID {
			continue
		}
		ack.Status = result.Status
		ack.UpdatedAt = at
		return
	}
	session.VoiceAcknowledgements = append(session.VoiceAcknowledgements, VoiceAcknowledgement{
		OperationID: result.OperationID, Status: result.Status, AcceptedAt: at, UpdatedAt: at,
		BaselineCount: result.TranscriptBaselineCount,
		BaselineKnown: result.TranscriptBaselineKnown, Ordinal: result.TranscriptOrdinal,
	})
	if len(session.VoiceAcknowledgements) > maxVoiceAcknowledgements {
		session.VoiceAcknowledgements = append(
			[]VoiceAcknowledgement(nil),
			session.VoiceAcknowledgements[len(session.VoiceAcknowledgements)-maxVoiceAcknowledgements:]...,
		)
	}
}

func (s *State) RenameSession(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
	name string,
	at time.Time,
) error {
	return s.RenameSessionWithFormat(
		actorID, ref, expectedRevision, name, SessionNameFormatVersion, at,
	)
}

func (s *State) RenameSessionWithFormat(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
	name string,
	formatVersion int,
	at time.Time,
) error {
	if !s.CanPerformSessionAction(actorID, ref, ActionRename) {
		return ErrAccessDenied
	}
	session, err := s.mutableLiveSession(ref)
	if err != nil {
		return err
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	if formatVersion == SessionNameFormatVersion {
		name, err = NormalizeSessionName(name)
		if err != nil {
			return err
		}
		if s.sessionNameTaken(session.OwnerID, ref, name) {
			return fmt.Errorf("%w: session name already exists", ErrAlreadyExists)
		}
	} else {
		// Commands written before name_format_version became part of the Raft
		// payload must replay with their historical validation and remain marked
		// legacy. Startup migration will replace them from the first prompt.
		formatVersion = 0
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 64 || strings.ContainsAny(name, "\r\n\t") {
			return fmt.Errorf("%w: session name is invalid", ErrInvalidState)
		}
	}
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	session.Name = name
	session.NameFormatVersion = formatVersion
	session.LastEventAt = at
	session.Revision++
	s.Sessions[ref.Key()] = session
	return nil
}

func (s *State) CloseSession(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
	archiveID string,
	at time.Time,
) error {
	if !s.CanPerformSessionAction(actorID, ref, ActionClose) {
		return ErrAccessDenied
	}
	session, err := s.mutableLiveSession(ref)
	if err != nil {
		return err
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	if session.RuntimePhase != RuntimeStarting && session.RuntimePhase != RuntimeIdle &&
		session.RuntimePhase != RuntimeWaitingInput {
		return ErrInvalidState
	}
	if strings.TrimSpace(archiveID) == "" || len(archiveID) > 128 || strings.ContainsAny(archiveID, "/\\") {
		return fmt.Errorf("%w: archive id is invalid", ErrInvalidState)
	}
	if err := archiveSession(&session, at, ArchiveManual); err != nil {
		return err
	}
	session.ArchiveID = archiveID
	session.ArchiveReady = false
	s.Sessions[ref.Key()] = session
	s.clearDeferredInputs(ref)
	s.clearBackgroundSession(ref)
	s.repairNavigationAfterClosed(ref)
	return nil
}

func (s *State) CompleteSessionArchive(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
	archiveID string,
	at time.Time,
) error {
	if !s.CanPerformSessionAction(actorID, ref, ActionArchive) {
		return ErrAccessDenied
	}
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	if session.State != SessionArchived || session.ArchiveID != archiveID ||
		session.ArchiveReason != ArchiveManual {
		return ErrInvalidState
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	session.ArchiveReady = true
	session.LastEventAt = at
	session.Revision++
	s.Sessions[ref.Key()] = session
	return nil
}

func (s *State) RestoreSession(
	actorID UserID,
	ref SessionRef,
	expectedRevision uint64,
	at time.Time,
) error {
	if !s.CanPerformSessionAction(actorID, ref, ActionRestore) {
		return ErrAccessDenied
	}
	session, ok := s.Sessions[ref.Key()]
	if !ok {
		return ErrNotFound
	}
	node, nodeOK := s.Nodes[session.NodeID]
	if session.State != SessionArchived || !session.ArchiveReady ||
		strings.TrimSpace(session.ArchiveID) == "" ||
		strings.TrimSpace(session.ProviderSessionID) == "" ||
		strings.TrimSpace(session.Workdir) == "" || !nodeOK || node.Status != NodeOnline ||
		!node.BackendExecutionAllowed() {
		return ErrInvalidState
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	if session.RuntimeGeneration == math.MaxUint64 || session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session counter exhausted", ErrInvalidState)
	}
	session.State = SessionLive
	session.RuntimePhase = RuntimeDegraded
	session.RuntimeIssue = ""
	session.InteractivePrompt = nil
	session.ArchiveDescription = nil
	session.DescriptionVersion = 0
	session.RuntimeGeneration++
	session.ResumePending = true
	session.LiveSinceAt = at
	session.LastEventAt = at
	session.Revision++
	s.Sessions[ref.Key()] = session
	return nil
}

// ArchiveSession records a coordinator-owned lifecycle transition. User
// initiated closure must use CloseSession so authorization cannot be skipped.
func (s *State) ArchiveSession(
	ref SessionRef,
	expectedRevision uint64,
	reason ArchiveReason,
	at time.Time,
) error {
	session, err := s.mutableLiveSession(ref)
	if err != nil {
		return err
	}
	if err := requireRevision(session, expectedRevision); err != nil {
		return err
	}
	if reason == ArchiveManual || !validArchiveReason(reason) {
		return fmt.Errorf("%w: unsupported automatic archive reason %q", ErrInvalidState, reason)
	}
	if err := archiveSession(&session, at, reason); err != nil {
		return err
	}
	s.Sessions[ref.Key()] = session
	s.clearDeferredInputs(ref)
	s.clearBackgroundSession(ref)
	s.repairNavigationAfterUnavailable(ref)
	return nil
}
