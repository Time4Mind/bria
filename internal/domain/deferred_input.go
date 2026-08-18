package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type DeferredInputKind string

const (
	DeferredInputText     DeferredInputKind = "text"
	DeferredInputPhoto    DeferredInputKind = "photo"
	DeferredInputDocument DeferredInputKind = "document"
	DeferredInputVoice    DeferredInputKind = "voice"
)

type DeferredInputFile struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	UniqueID string `json:"unique_id"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// DeferredSessionInput contains only bounded text and external transport
// identifiers. Media bytes and transport credentials never enter Raft state.
type DeferredSessionInput struct {
	OperationID        string            `json:"operation_id"`
	ActorID            UserID            `json:"actor_id"`
	Session            SessionRef        `json:"session"`
	ExpectedGeneration uint64            `json:"expected_generation"`
	Kind               DeferredInputKind `json:"kind"`
	Text               string            `json:"text,omitempty"`
	Caption            string            `json:"caption,omitempty"`
	VoiceBackend       string            `json:"voice_backend,omitempty"`
	VoiceLanguage      string            `json:"voice_language,omitempty"`
	File               DeferredInputFile `json:"file,omitempty"`
	QueuedAt           time.Time         `json:"queued_at"`
}

func (input DeferredSessionInput) Validate() error {
	if strings.TrimSpace(input.OperationID) == "" || len(input.OperationID) > 128 {
		return fmt.Errorf("%w: deferred operation id is invalid", ErrInvalidState)
	}
	if input.ActorID <= 0 || input.Session.Validate() != nil || input.ExpectedGeneration == 0 {
		return fmt.Errorf("%w: deferred input identity is invalid", ErrInvalidState)
	}
	switch input.Kind {
	case DeferredInputText:
		if strings.TrimSpace(input.Text) == "" || len(input.Text) > 16<<10 || input.Caption != "" || input.File.ID != "" {
			return fmt.Errorf("%w: deferred text is invalid", ErrInvalidState)
		}
	case DeferredInputPhoto, DeferredInputDocument, DeferredInputVoice:
		if input.File.Provider != "telegram" || strings.TrimSpace(input.File.ID) == "" ||
			strings.TrimSpace(input.File.UniqueID) == "" || input.File.Size < 0 ||
			len(input.File.ID) > 512 || len(input.File.UniqueID) > 256 ||
			len(input.File.Name) > 255 || len(input.File.MIMEType) > 255 || len(input.Caption) > 16<<10 {
			return fmt.Errorf("%w: deferred external input is invalid", ErrInvalidState)
		}
		if input.Kind != DeferredInputVoice &&
			(input.VoiceBackend != "" || input.VoiceLanguage != "") {
			return fmt.Errorf("%w: voice metadata on non-voice input", ErrInvalidState)
		}
		if input.Kind == DeferredInputVoice && input.VoiceBackend != "" &&
			input.VoiceBackend != "auto" && input.VoiceBackend != "whisper" &&
			input.VoiceBackend != "apple" && input.VoiceBackend != "off" {
			return fmt.Errorf("%w: unsupported deferred voice backend", ErrInvalidState)
		}
		if input.Kind == DeferredInputVoice && input.VoiceLanguage != "" &&
			input.VoiceLanguage != "auto" && input.VoiceLanguage != "ru" &&
			input.VoiceLanguage != "en" && input.VoiceLanguage != "zh" {
			return fmt.Errorf("%w: unsupported deferred voice language", ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unsupported deferred input kind", ErrInvalidState)
	}
	return nil
}

func (s *State) QueueDeferredSessionInput(input DeferredSessionInput, at time.Time) error {
	input.QueuedAt = at
	if err := input.Validate(); err != nil {
		return err
	}
	if !s.CanPerformSessionAction(input.ActorID, input.Session, ActionSendInput) {
		return ErrAccessDenied
	}
	session, ok := s.Sessions[input.Session.Key()]
	node, nodeOK := s.Nodes[input.Session.NodeID]
	queue := s.DeferredInputs[input.Session.Key()]
	if !ok || !session.IsLive() || !nodeOK || !node.Enabled() ||
		(node.Status == NodeOnline && len(queue) == 0) || session.RuntimeGeneration != input.ExpectedGeneration {
		return ErrInvalidState
	}
	for _, current := range queue {
		if current.OperationID == input.OperationID {
			return nil
		}
	}
	preferences, ok := s.Preferences[input.ActorID]
	if !ok {
		preferences = DefaultUserPreferences()
	}
	if len(queue) >= preferences.EffectiveOfflineInputQueueLimit() {
		return ErrQueueFull
	}
	if session.Revision == math.MaxUint64 {
		return fmt.Errorf("%w: session revision exhausted", ErrInvalidState)
	}
	s.DeferredInputs[input.Session.Key()] = append(queue, input)
	session.LastEventAt = at
	session.LastOperation = &SessionOperationResult{
		OperationID: input.OperationID, Action: ActionSendInput, Status: OperationQueued,
		Detail: "waiting for node recovery", At: at,
	}
	session.Revision++
	s.Sessions[input.Session.Key()] = session
	if s.Navigation.SessionActivityByUser[input.ActorID] == nil {
		s.Navigation.SessionActivityByUser[input.ActorID] = make(map[string]time.Time)
	}
	s.Navigation.SessionActivityByUser[input.ActorID][input.Session.Key()] = at
	return nil
}

// ResolveDeferredSessionInput removes only the current head. This invariant
// prevents retries or leadership changes from reordering a session's inputs.
func (s *State) ResolveDeferredSessionInput(ref SessionRef, operationID string, failed bool, detail string, at time.Time) error {
	queue := s.DeferredInputs[ref.Key()]
	if len(queue) == 0 || queue[0].OperationID != operationID {
		return ErrStaleOperation
	}
	if len(detail) > 512 {
		return fmt.Errorf("%w: deferred result detail is too long", ErrInvalidState)
	}
	if len(queue) == 1 {
		delete(s.DeferredInputs, ref.Key())
	} else {
		s.DeferredInputs[ref.Key()] = append([]DeferredSessionInput(nil), queue[1:]...)
	}
	if failed {
		session, ok := s.Sessions[ref.Key()]
		if ok && session.IsLive() && session.Revision < math.MaxUint64 {
			session.LastEventAt = at
			session.LastOperation = &SessionOperationResult{
				OperationID: operationID, Action: ActionSendInput, Status: OperationFailed,
				Detail: detail, At: at,
			}
			session.Revision++
			s.Sessions[ref.Key()] = session
		}
	}
	return nil
}

func (s *State) clearDeferredInputs(ref SessionRef) {
	delete(s.DeferredInputs, ref.Key())
}
