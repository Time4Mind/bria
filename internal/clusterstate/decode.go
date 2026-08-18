package clusterstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func decodeAnd[T any](
	payload json.RawMessage,
	strict bool,
	target *T,
	action func() error,
) error {
	var err error
	if strict {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(target)
		if err == nil && decoder.Decode(&struct{}{}) != io.EOF {
			err = errors.New("payload contains trailing JSON values")
		}
	} else {
		err = json.Unmarshal(payload, target)
	}
	if err != nil {
		return fmt.Errorf("decode command payload: %w", err)
	}
	return action()
}
