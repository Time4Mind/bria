package interactionflow

import (
	"strings"
	"unicode/utf8"

	"bria/internal/interactionstore"
	"bria/internal/runtimeprotocol"
)

type Phase = interactionstore.Phase
type Operation = interactionstore.Operation
type ConsumedSource = interactionstore.ConsumedSource
type Store = interactionstore.Store
type FileStore = interactionstore.FileStore
type MemoryStore = interactionstore.MemoryStore

const (
	PhasePrepared                  = interactionstore.PhasePrepared
	PhaseSendUnknown               = interactionstore.PhaseSendUnknown
	PhaseWaiting                   = interactionstore.PhaseWaiting
	PhaseWaitingText               = interactionstore.PhaseWaitingText
	PhaseSecretDeletionUnknown     = interactionstore.PhaseSecretDeletionUnknown
	PhaseResponseReady             = interactionstore.PhaseResponseReady
	PhaseProviderResponseUnknown   = interactionstore.PhaseProviderResponseUnknown
	PhaseProviderResponseConfirmed = interactionstore.PhaseProviderResponseConfirmed
)

var (
	ErrOperationExists   = interactionstore.ErrOperationExists
	ErrImmutableIdentity = interactionstore.ErrImmutableIdentity
	ErrInvalidOperation  = interactionstore.ErrInvalidOperation
	ErrInvalidTransition = interactionstore.ErrInvalidTransition
	ErrStoreExhausted    = interactionstore.ErrStoreExhausted
	OpenFileStore        = interactionstore.OpenFileStore
	NewMemoryStore       = interactionstore.NewMemoryStore
)

func saneText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func cloneOperation(operation Operation) Operation {
	clone := operation
	clone.Request = cloneRequest(operation.Request)
	clone.Answers = cloneAnswers(operation.Answers)
	if operation.Response != nil {
		response := *operation.Response
		response.Answers = cloneAnswers(operation.Response.Answers)
		clone.Response = &response
	}
	return clone
}

func cloneRequest(request runtimeprotocol.InteractionRequest) runtimeprotocol.InteractionRequest {
	clone := request
	clone.Decisions = append([]runtimeprotocol.ApprovalDecision(nil), request.Decisions...)
	if request.Questions != nil {
		clone.Questions = make([]runtimeprotocol.Question, len(request.Questions))
		for index, question := range request.Questions {
			clone.Questions[index] = question
			clone.Questions[index].Options = append([]runtimeprotocol.Option(nil), question.Options...)
		}
	}
	return clone
}

func cloneAnswers(answers map[string][]string) map[string][]string {
	if answers == nil {
		return nil
	}
	clone := make(map[string][]string, len(answers))
	for id, values := range answers {
		clone[id] = append([]string(nil), values...)
	}
	return clone
}
