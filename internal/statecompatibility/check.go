// Package statecompatibility performs offline read-only compatibility checks
// for every durable runtime document that constrains a safe update or rollback.
package statecompatibility

import (
	"errors"

	"bria/internal/config"
	"bria/internal/messagejournal"
	"bria/internal/storage"
)

func CheckConfig(path string) error {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return errors.New("configuration cannot be validated")
	}
	if err := storage.ValidateSessionFile(cfg.StatePath); err != nil {
		return errors.New("state is unreadable or incompatible with this version")
	}
	journalLimits := messagejournal.DefaultLimits()
	journalLimits.MaxPendingInputsPerSession = journalLimits.MaxInputsPerSession
	if _, err := messagejournal.Open(cfg.StatePath+".message-journal.json", journalLimits); err != nil {
		return errors.New("message journal is unreadable or incompatible with this version")
	}
	return nil
}
