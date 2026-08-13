package clusterstate

import (
	"encoding/json"
	"fmt"
)

func decodeAnd[T any](payload json.RawMessage, target *T, action func() error) error {
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode command payload: %w", err)
	}
	return action()
}
