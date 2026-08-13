//go:build windows

package providerauth

import (
	"context"
	"errors"
)

func launchClaude(context.Context, string) (Process, error) {
	return nil, errors.New("Claude interactive authentication is not yet supported on Windows")
}
