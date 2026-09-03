// Package coordinatorbundle defines the credential-free, complete state moved
// during an explicit coordinator handoff.
package coordinatorbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"bria/internal/computer"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/settings"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

const Version = 1

var ErrInvalid = fmt.Errorf("invalid coordinator bundle")

type Route struct {
	TelegramMessageID int64             `json:"telegram_message_id"`
	SessionID         domain.SessionID  `json:"session_id"`
	ComputerID        domain.ComputerID `json:"computer_id"`
}

// JournalSession carries the durable high-water mark; terminal records are tombstones proving that no sequence was dropped.
type JournalSession struct {
	SessionID    domain.SessionID `json:"session_id"`
	NextSequence uint64           `json:"next_sequence"`
}

type TelegramScope struct {
	BotID         int64 `json:"bot_id"`
	OwnerUserID   int64 `json:"owner_user_id"`
	PrivateChatID int64 `json:"private_chat_id"`
}

// Bundle contains semantic state only. Credentials, provider auth, Telegram
// token and callback signing keys deliberately have no typed field here.
type Bundle struct {
	Version       int                          `json:"version"`
	Catalog       computer.CatalogSnapshot     `json:"catalog"`
	Routes        []Route                      `json:"routes"`
	Settings      settings.Snapshot            `json:"settings"`
	Sessions      []domain.SessionSnapshot     `json:"sessions"`
	TelegramScope TelegramScope                `json:"telegram_scope"`
	TelegramUI    telegramstate.State          `json:"telegram_ui"`
	Journals      []JournalSession             `json:"journals"`
	Inputs        []messagejournal.Input       `json:"inputs"`
	Outputs       []messagejournal.Output      `json:"outputs"`
	Checkpoint    coordinator.StoredCheckpoint `json:"checkpoint"`
	// CallbackVerificationKeyID is a SHA-256 fingerprint, never key material.
	CallbackVerificationKeyID string                                    `json:"callback_verification_key_id"`
	CallbackRegistry          telegrampipeline.CallbackRegistrySnapshot `json:"callback_registry"`
	CallbackOperations        telegramflow.CallbackStateSnapshot        `json:"callback_operations"`
}

func (bundle Bundle) Validate() error {
	if bundle.Version != Version {
		return invalid("version")
	}
	if _, err := computer.RestoreCatalog(bundle.Catalog); err != nil {
		return invalid("catalog: %v", err)
	}
	if err := bundle.Settings.Settings.Validate(); err != nil {
		return invalid("settings: %v", err)
	}
	if !bundle.Checkpoint.Checkpoint.Initialized || bundle.Checkpoint.Checkpoint.NextUpdateID < 0 || bundle.Checkpoint.Revision == 0 {
		return invalid("checkpoint")
	}
	if bundle.TelegramScope.BotID <= 0 || bundle.TelegramScope.OwnerUserID <= 0 || bundle.TelegramScope.PrivateChatID <= 0 {
		return invalid("telegram scope")
	}
	if outbound := bundle.Checkpoint.Checkpoint.Outbound; outbound != nil && outbound.Status.ConversationID != bundle.TelegramScope.PrivateChatID {
		return invalid("checkpoint Telegram scope")
	}
	if err := bundle.TelegramUI.Validate(); err != nil {
		return invalid("telegram UI: %v", err)
	}
	if err := bundle.CallbackRegistry.Validate(); err != nil {
		return invalid("callback registry: %v", err)
	}
	if err := bundle.CallbackOperations.Validate(); err != nil {
		return invalid("callback operations: %v", err)
	}
	if decoded, err := hex.DecodeString(bundle.CallbackVerificationKeyID); err != nil || len(decoded) != sha256.Size {
		return invalid("callback verification key id")
	}
	computers := make(map[domain.ComputerID]struct{}, len(bundle.Catalog.Computers))
	for _, record := range bundle.Catalog.Computers {
		computers[record.ID] = struct{}{}
	}
	sessions := make(map[domain.SessionID]domain.SessionSnapshot, len(bundle.Sessions))
	intents := make(map[domain.IntentID]struct{}, len(bundle.Sessions))
	for _, snapshot := range bundle.Sessions {
		if _, duplicate := sessions[snapshot.ID]; duplicate {
			return invalid("duplicate session")
		}
		if _, duplicate := intents[snapshot.IntentID]; duplicate {
			return invalid("duplicate session intent")
		}
		if _, err := domain.RestoreSession(snapshot); err != nil {
			return invalid("session: %v", err)
		}
		if _, exists := computers[snapshot.ComputerID]; !exists {
			return invalid("session computer")
		}
		sessions[snapshot.ID], intents[snapshot.IntentID] = snapshot, struct{}{}
	}
	for id := range bundle.TelegramUI.Cards {
		if _, exists := sessions[id]; !exists {
			return invalid("card session")
		}
		if carrier := bundle.TelegramUI.Cards[id].Carrier; carrier.ChatID != 0 && carrier.ChatID != bundle.TelegramScope.PrivateChatID {
			return invalid("card Telegram scope")
		}
	}
	routeIDs := make(map[int64]struct{}, len(bundle.Routes))
	for _, route := range bundle.Routes {
		if route.TelegramMessageID <= 0 || strings.TrimSpace(string(route.SessionID)) == "" {
			return invalid("route")
		}
		if _, duplicate := routeIDs[route.TelegramMessageID]; duplicate {
			return invalid("duplicate route")
		}
		if _, exists := sessions[route.SessionID]; !exists {
			return invalid("route session")
		}
		if _, exists := computers[route.ComputerID]; !exists {
			return invalid("route computer")
		}
		routeIDs[route.TelegramMessageID] = struct{}{}
	}
	if err := validateJournals(bundle, sessions); err != nil {
		return err
	}
	for sessionID, presentation := range bundle.CallbackRegistry.Presentations {
		if presentation.Carrier.ChatID != bundle.TelegramScope.PrivateChatID {
			return invalid("callback presentation Telegram scope")
		}
		if sessionID == telegramui.GlobalSurfaceID {
			continue
		}
		if _, exists := sessions[sessionID]; !exists {
			return invalid("callback presentation session")
		}
		card, exists := bundle.TelegramUI.Cards[sessionID]
		if !exists || card.Carrier != presentation.Carrier {
			return invalid("callback presentation carrier")
		}
	}
	for _, operation := range bundle.CallbackOperations.Operations {
		if operation.Plan.Carrier.ChatID != bundle.TelegramScope.PrivateChatID || operation.Prepared != nil && operation.Prepared.Status.ConversationID != bundle.TelegramScope.PrivateChatID {
			return invalid("callback operation Telegram scope")
		}
		presentation, exists := bundle.CallbackRegistry.Presentations[operation.Plan.SessionID]
		if !exists || presentation.Carrier != operation.Plan.Carrier {
			return invalid("callback operation carrier")
		}
		if operation.Plan.SessionID == telegramui.GlobalSurfaceID {
			continue
		}
		if _, exists := sessions[operation.Plan.SessionID]; !exists {
			return invalid("callback operation session")
		}
	}
	for _, operation := range bundle.CallbackOperations.Statuses {
		if operation.Status.ConversationID != bundle.TelegramScope.PrivateChatID || operation.Prepared != nil && operation.Prepared.Status.ConversationID != bundle.TelegramScope.PrivateChatID {
			return invalid("status operation Telegram scope")
		}
	}
	return nil
}

func validateJournals(bundle Bundle, sessions map[domain.SessionID]domain.SessionSnapshot) error {
	high := make(map[domain.SessionID]uint64, len(bundle.Journals))
	for _, journal := range bundle.Journals {
		if journal.NextSequence == 0 {
			return invalid("journal high-water")
		}
		if _, exists := sessions[journal.SessionID]; !exists {
			return invalid("journal session")
		}
		if _, duplicate := high[journal.SessionID]; duplicate {
			return invalid("duplicate journal session")
		}
		high[journal.SessionID] = journal.NextSequence
	}
	sequence := make(map[domain.SessionID]map[uint64]struct{}, len(high))
	inputIDs := make(map[string]struct{}, len(bundle.Inputs))
	for _, input := range bundle.Inputs {
		if strings.TrimSpace(input.MessageID) == "" || input.Sequence == 0 || len(input.Payload) > 1<<20 || input.Lease.Owner != "" || !input.Lease.Until.IsZero() {
			return invalid("input")
		}
		if _, duplicate := inputIDs[input.MessageID]; duplicate {
			return invalid("duplicate input")
		}
		inputIDs[input.MessageID] = struct{}{}
		if !validInputPhase(input.Phase) {
			return invalid("input phase")
		}
		id := domain.SessionID(input.SessionID)
		if _, exists := high[id]; !exists {
			return invalid("input journal")
		}
		if sequence[id] == nil {
			sequence[id] = make(map[uint64]struct{})
		}
		if _, duplicate := sequence[id][input.Sequence]; duplicate {
			return invalid("duplicate sequence")
		}
		sequence[id][input.Sequence] = struct{}{}
	}
	outputIDs := make(map[string]struct{}, len(bundle.Outputs))
	for _, output := range bundle.Outputs {
		if strings.TrimSpace(output.OperationID) == "" || output.Sequence == 0 || len(output.Payload) > 1<<20 || output.Lease.Owner != "" || !output.Lease.Until.IsZero() {
			return invalid("output")
		}
		if _, duplicate := outputIDs[output.OperationID]; duplicate {
			return invalid("duplicate output")
		}
		outputIDs[output.OperationID] = struct{}{}
		if !validOutputPhase(output.Phase) {
			return invalid("output phase")
		}
		id := domain.SessionID(output.SessionID)
		if _, exists := high[id]; !exists {
			return invalid("output journal")
		}
		if sequence[id] == nil {
			sequence[id] = make(map[uint64]struct{})
		}
		if _, duplicate := sequence[id][output.Sequence]; duplicate {
			return invalid("duplicate sequence")
		}
		sequence[id][output.Sequence] = struct{}{}
	}
	for id, last := range high {
		if uint64(len(sequence[id])) != last {
			return invalid("journal sequence count")
		}
		for number := uint64(1); number <= last; number++ {
			if _, exists := sequence[id][number]; !exists {
				return invalid("journal sequence gap")
			}
		}
	}
	return nil
}

func validInputPhase(phase messagejournal.InputPhase) bool {
	switch phase {
	case messagejournal.InputPending, messagejournal.InputAccepted, messagejournal.InputCompleted, messagejournal.InputFailed, messagejournal.InputUnknown:
		return true
	}
	return false
}
func validOutputPhase(phase messagejournal.OutputPhase) bool {
	switch phase {
	case messagejournal.OutputPending, messagejournal.OutputConfirmed, messagejournal.OutputFailed, messagejournal.OutputUnknown:
		return true
	}
	return false
}

func (bundle Bundle) Digest() (string, error) {
	if err := bundle.Validate(); err != nil {
		return "", err
	}
	canonical := bundle
	canonical.Routes = append([]Route(nil), bundle.Routes...)
	canonical.Sessions = append([]domain.SessionSnapshot(nil), bundle.Sessions...)
	canonical.Journals = append([]JournalSession(nil), bundle.Journals...)
	sort.Slice(canonical.Routes, func(i, j int) bool {
		return canonical.Routes[i].TelegramMessageID < canonical.Routes[j].TelegramMessageID
	})
	sort.Slice(canonical.Sessions, func(i, j int) bool { return canonical.Sessions[i].ID < canonical.Sessions[j].ID })
	sort.Slice(canonical.Journals, func(i, j int) bool { return canonical.Journals[i].SessionID < canonical.Journals[j].SessionID })
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func invalid(format string, values ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, values...))
}
