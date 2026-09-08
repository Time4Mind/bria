package notificationstate

import (
	"bytes"
	"encoding/json"
	"errors"
)

// encoding/json otherwise silently accepts repeated keys, including conflicting
// receipts or plans. Inspect tokens before decoding the typed bounded state.
func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 16 {
			return errors.New("invalid part receipt JSON nesting")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return nil
		}
		keys := make(map[string]bool)
		for decoder.More() {
			if delimiter == '{' {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("invalid part receipt JSON key")
				}
				keys[name] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	return value(0)
}
