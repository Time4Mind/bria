// Package sessionid supplies logical Bria session identifiers.
package sessionid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"

	"bria/internal/domain"
)

// Source creates RFC 4122 UUID version 4 identifiers. Access to reader is
// serialized because injected readers are not necessarily concurrency-safe.
type Source struct {
	mu     sync.Mutex
	reader io.Reader
}

func New() *Source {
	return &Source{reader: rand.Reader}
}

func NewWithReader(reader io.Reader) (*Source, error) {
	if reader == nil {
		return nil, errors.New("session id random reader is required")
	}
	return &Source{reader: reader}, nil
}

func (source *Source) NewSessionID(ctx context.Context) (domain.SessionID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var uuid [16]byte
	if _, err := io.ReadFull(source.reader, uuid[:]); err != nil {
		return "", fmt.Errorf("read session id randomness: %w", err)
	}
	uuid[6] = uuid[6]&0x0f | 0x40
	uuid[8] = uuid[8]&0x3f | 0x80
	return formatUUID(uuid), nil
}

func formatUUID(uuid [16]byte) domain.SessionID {
	var encoded [36]byte
	hex.Encode(encoded[0:8], uuid[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], uuid[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], uuid[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], uuid[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], uuid[10:16])
	return domain.SessionID(encoded[:])
}
