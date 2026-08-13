// Package platform exposes narrowly scoped host-platform facts.
//
// Boot identity is deliberately independent from CPU architecture and init
// system detection: an arm64 host may use systemd, launchd, or no init at all.
package platform

import (
	"context"
	"errors"
)

var ErrBootIDUnsupported = errors.New("boot identity is unsupported on this platform")

// BootIDProvider returns an identifier that remains stable for one OS boot.
type BootIDProvider interface {
	Current(context.Context) (string, error)
}

type UnsupportedBootIDProvider struct{}

func (UnsupportedBootIDProvider) Current(context.Context) (string, error) {
	return "", ErrBootIDUnsupported
}
