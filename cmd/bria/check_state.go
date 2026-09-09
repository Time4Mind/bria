package main

import (
	"errors"

	"bria/internal/config"
	"bria/internal/storage"
)

// checkState validates the current file with this binary's exact decoder.
// It neither starts providers nor acquires the live service lock or writes data.
func checkState(path string) error {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return errors.New("configuration cannot be validated")
	}
	if err := storage.ValidateSessionFile(cfg.StatePath); err != nil {
		return errors.New("state is unreadable or incompatible with this version")
	}
	return nil
}
