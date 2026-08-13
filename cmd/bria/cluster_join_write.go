package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func writeConfigNew(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := writeExclusive(path, append(encoded, '\n')); err != nil {
		return fmt.Errorf("write joined node config: %w", err)
	}
	return nil
}
