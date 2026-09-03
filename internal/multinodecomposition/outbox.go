// Package multinodecomposition composes the durable multi-computer network roles.
package multinodecomposition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"bria/internal/nodelink"
)

var (
	ErrInvalidComposition = errors.New("invalid multi-node composition")
	ErrEventConflict      = errors.New("event operation id was reused for different content")
)

const maxOutboxBytes = 32 << 20
const maxOutboxEvents = 4096

type outboxSnapshot struct {
	Version uint16              `json:"version"`
	Events  []nodelink.Envelope `json:"events"`
}

type FileEventOutbox struct {
	mu       sync.Mutex
	delivery sync.Mutex
	path     string
	events   []nodelink.Envelope
}

func OpenFileEventOutbox(path string) (*FileEventOutbox, error) {
	if strings.TrimSpace(path) == "" || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return nil, ErrInvalidComposition
	}
	outbox := &FileEventOutbox{path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return outbox, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxOutboxBytes {
		return nil, ErrInvalidComposition
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snapshot outboxSnapshot
	if err := decodeOutbox(data, &snapshot); err != nil || snapshot.Version != nodelink.ProtocolVersion || snapshot.Events == nil || len(snapshot.Events) > maxOutboxEvents {
		return nil, ErrInvalidComposition
	}
	seen := make(map[string][]byte, len(snapshot.Events))
	for _, event := range snapshot.Events {
		encoded, valid := validEvent(event)
		if !valid {
			return nil, ErrInvalidComposition
		}
		if _, duplicate := seen[event.OperationID]; duplicate {
			return nil, ErrInvalidComposition
		}
		seen[event.OperationID] = encoded
		outbox.events = append(outbox.events, cloneEnvelope(event))
	}
	return outbox, nil
}

func (outbox *FileEventOutbox) Enqueue(ctx context.Context, event nodelink.Envelope) error {
	if outbox == nil || ctx == nil {
		return ErrInvalidComposition
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, valid := validEvent(event)
	if !valid {
		return ErrInvalidComposition
	}
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	for _, existing := range outbox.events {
		if existing.OperationID != event.OperationID {
			continue
		}
		current, _ := json.Marshal(existing)
		if !bytes.Equal(current, encoded) {
			return ErrEventConflict
		}
		return nil
	}
	if len(outbox.events) >= maxOutboxEvents {
		return ErrInvalidComposition
	}
	outbox.events = append(outbox.events, cloneEnvelope(event))
	if err := outbox.persistLocked(); err != nil {
		outbox.events = outbox.events[:len(outbox.events)-1]
		return err
	}
	return nil
}

func (outbox *FileEventOutbox) Pending() []nodelink.Envelope {
	if outbox == nil {
		return nil
	}
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	result := make([]nodelink.Envelope, len(outbox.events))
	for index, event := range outbox.events {
		result[index] = cloneEnvelope(event)
	}
	return result
}

// Deliver replays events in order. It removes an event only after receiving an
// exact acknowledgement over the mutually authenticated channel.
func (outbox *FileEventOutbox) Deliver(ctx context.Context, channel *nodelink.SecureChannel) error {
	if outbox == nil || ctx == nil || channel == nil {
		return ErrInvalidComposition
	}
	outbox.delivery.Lock()
	defer outbox.delivery.Unlock()
	for {
		outbox.mu.Lock()
		if len(outbox.events) == 0 {
			outbox.mu.Unlock()
			return nil
		}
		event := cloneEnvelope(outbox.events[0])
		outbox.mu.Unlock()
		identity := channel.Identity()
		if !identity.MutuallyAuthenticated || identity.LocalComputerID != event.SourceComputerID || identity.PeerComputerID != event.CoordinatorID || identity.ExecutorComputerID != event.SourceComputerID {
			return ErrInvalidComposition
		}
		if err := channel.WriteEnvelope(ctx, event); err != nil {
			return err
		}
		receipt, err := channel.ReadEnvelope(ctx)
		if err != nil {
			return err
		}
		if receipt.Kind != nodelink.KindAcknowledgement || receipt.OperationID != event.OperationID || receipt.Generation != event.Generation || receipt.CoordinatorID != event.CoordinatorID || receipt.SourceComputerID != event.CoordinatorID || receipt.TargetComputerID != event.SourceComputerID {
			return ErrInvalidComposition
		}
		outbox.mu.Lock()
		if len(outbox.events) == 0 || outbox.events[0].OperationID != event.OperationID {
			outbox.mu.Unlock()
			return ErrEventConflict
		}
		previous := append([]nodelink.Envelope(nil), outbox.events...)
		outbox.events = outbox.events[1:]
		if err := outbox.persistLocked(); err != nil {
			outbox.events = previous
			outbox.mu.Unlock()
			return err
		}
		outbox.mu.Unlock()
	}
}

func (outbox *FileEventOutbox) persistLocked() error {
	data, err := json.Marshal(outboxSnapshot{Version: nodelink.ProtocolVersion, Events: outbox.events})
	if err != nil || len(data) > maxOutboxBytes {
		return ErrInvalidComposition
	}
	data = append(data, '\n')
	directory := filepath.Dir(outbox.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".event-outbox-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, outbox.path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	verified, err := os.ReadFile(outbox.path)
	if err != nil || !bytes.Equal(verified, data) {
		return ErrInvalidComposition
	}
	return nil
}

func validEvent(event nodelink.Envelope) ([]byte, bool) {
	if event.Version != nodelink.ProtocolVersion || event.Kind != nodelink.KindEvent || strings.TrimSpace(event.OperationID) == "" || event.Generation == 0 || strings.TrimSpace(string(event.CoordinatorID)) == "" || strings.TrimSpace(string(event.SourceComputerID)) == "" || event.TargetComputerID != event.CoordinatorID || event.SourceComputerID == event.CoordinatorID || len(event.Payload) > int(nodelink.DefaultMaxFrameBytes) || !json.Valid(event.Payload) {
		return nil, false
	}
	encoded, err := json.Marshal(event)
	return encoded, err == nil
}

func cloneEnvelope(event nodelink.Envelope) nodelink.Envelope {
	event.Payload = append([]byte(nil), event.Payload...)
	return event
}

func decodeOutbox(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidComposition
	}
	return nil
}
