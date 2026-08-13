package telegrambot

import (
	"errors"
	"strings"
)

// redactedTransportCause keeps the error chain useful without retaining a
// Bot API URL containing the secret token.
func (c *Client) redactedTransportCause(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
}
