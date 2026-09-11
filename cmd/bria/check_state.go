package main

import (
	"bria/internal/statecompatibility"
)

// checkState validates the current file with this binary's exact decoder.
// It neither starts providers nor acquires the live service lock or writes data.
func checkState(path string) error {
	return statecompatibility.CheckConfig(path)
}
