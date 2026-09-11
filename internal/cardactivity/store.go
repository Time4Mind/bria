// Package cardactivity persists optional per-card activity metadata outside the rollback-compatible session file.
package cardactivity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

const maxSize = 1 << 20

type record struct {
	LastEventUnixNano int64  `json:"last_event_unix_nano"`
	Fingerprint       string `json:"fingerprint"`
}
type document struct {
	Version int                         `json:"version"`
	Cards   map[domain.SessionID]record `json:"cards"`
}

func MainState(source *telegramstate.State) *telegramstate.State {
	if source == nil {
		return nil
	}
	clone := source.Clone()
	for id, card := range clone.Cards {
		card.LastEventUnixNano = 0
		clone.Cards[id] = card
	}
	return &clone
}

func Apply(statePath string, state *telegramstate.State) error {
	if state == nil {
		return nil
	}
	data, err := os.ReadFile(sidecarPath(statePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || len(data) > maxSize {
		return errors.New("read card activity sidecar")
	}
	var stored document
	if json.Unmarshal(data, &stored) != nil || stored.Version != 1 || stored.Cards == nil {
		return errors.New("decode card activity sidecar")
	}
	for id, value := range stored.Cards {
		card, ok := state.Cards[id]
		if !ok || value.LastEventUnixNano <= 0 || value.Fingerprint != fingerprint(card) {
			continue
		}
		card.LastEventUnixNano = value.LastEventUnixNano
		state.Cards[id] = card
	}
	return nil
}

func Save(statePath string, state *telegramstate.State) (returnErr error) {
	stored := document{Version: 1, Cards: make(map[domain.SessionID]record)}
	if state != nil {
		for id, card := range state.Cards {
			if card.LastEventUnixNano > 0 {
				stored.Cards[id] = record{LastEventUnixNano: card.LastEventUnixNano, Fingerprint: fingerprint(card)}
			}
		}
	}
	data, err := json.Marshal(stored)
	if err != nil || len(data) > maxSize {
		return errors.New("encode card activity sidecar")
	}
	path := sidecarPath(statePath)
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath, open := temporary.Name(), true
	defer func() {
		if open {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	open = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace card activity sidecar: %w", err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}

func sidecarPath(statePath string) string { return statePath + ".card-activity.json" }

func fingerprint(card telegramstate.Card) string {
	data, _ := json.Marshal(struct {
		History                []string
		HistoryKeys            []string
		HistoryTurnKeys        []string
		HistoryKinds           []string
		PendingFinalOperations []string
	}{card.History, card.HistoryKeys, card.HistoryTurnKeys, card.HistoryKinds, card.PendingFinalOperations})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
