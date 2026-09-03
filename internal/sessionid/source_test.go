package sessionid_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionid"
)

func TestSourceGeneratesCanonicalUUIDv4FromInjectedReader(t *testing.T) {
	t.Parallel()

	source, err := sessionid.NewWithReader(bytes.NewReader([]byte{
		0x00, 0x01, 0x02, 0x03,
		0x04, 0x05,
		0x06, 0x07,
		0x08, 0x09,
		0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	}))
	if err != nil {
		t.Fatalf("NewWithReader() error = %v", err)
	}
	var _ app.SessionIDSource = source

	id, err := source.NewSessionID(context.Background())
	if err != nil {
		t.Fatalf("NewSessionID() error = %v", err)
	}
	if got, want := id, domain.SessionID("00010203-0405-4607-8809-0a0b0c0d0e0f"); got != want {
		t.Fatalf("NewSessionID() = %q, want %q", got, want)
	}
}

func TestSourceReadsExactlyOneUUIDAndPropagatesReaderError(t *testing.T) {
	t.Parallel()

	source, err := sessionid.NewWithReader(bytes.NewReader(make([]byte, 15)))
	if err != nil {
		t.Fatalf("NewWithReader() error = %v", err)
	}
	if _, err := source.NewSessionID(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("NewSessionID() error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestSourceRejectsNilReaderAndCanceledContext(t *testing.T) {
	t.Parallel()

	if _, err := sessionid.NewWithReader(nil); err == nil {
		t.Fatal("NewWithReader(nil) error = nil")
	}
	source, err := sessionid.NewWithReader(&failIfReadReader{t: t})
	if err != nil {
		t.Fatalf("NewWithReader() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.NewSessionID(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewSessionID() error = %v, want context.Canceled", err)
	}
}

func TestSourceSerializesInjectedReaderAndGeneratesUniqueUUIDs(t *testing.T) {
	t.Parallel()

	reader := &concurrencyDetectingReader{}
	source, err := sessionid.NewWithReader(reader)
	if err != nil {
		t.Fatalf("NewWithReader() error = %v", err)
	}

	const workers = 64
	start := make(chan struct{})
	ids := make(chan domain.SessionID, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			id, err := source.NewSessionID(context.Background())
			ids <- id
			errs <- err
		}()
	}
	close(start)
	group.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("NewSessionID() error = %v", err)
		}
	}
	if reader.concurrent.Load() {
		t.Fatal("injected reader was called concurrently")
	}
	unique := make(map[domain.SessionID]struct{}, workers)
	for id := range ids {
		assertCanonicalUUIDv4(t, id)
		unique[id] = struct{}{}
	}
	if got, want := len(unique), workers; got != want {
		t.Fatalf("unique ids = %d, want %d", got, want)
	}
}

func TestDefaultSourceGeneratesUUIDv4(t *testing.T) {
	t.Parallel()

	source := sessionid.New()
	id, err := source.NewSessionID(context.Background())
	if err != nil {
		t.Fatalf("NewSessionID() error = %v", err)
	}
	assertCanonicalUUIDv4(t, id)
}

func assertCanonicalUUIDv4(t *testing.T, id domain.SessionID) {
	t.Helper()
	value := string(id)
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		t.Fatalf("session id %q is not canonical UUID format", id)
	}
	if value[14] != '4' {
		t.Fatalf("session id %q version nibble = %q, want 4", id, value[14])
	}
	if !strings.ContainsRune("89ab", rune(value[19])) {
		t.Fatalf("session id %q variant nibble = %q, want 8, 9, a, or b", id, value[19])
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", character) {
			t.Fatalf("session id %q contains non-lowercase-hex character %q", id, character)
		}
	}
}

type failIfReadReader struct {
	t *testing.T
}

func (reader *failIfReadReader) Read([]byte) (int, error) {
	reader.t.Fatal("reader called for canceled context")
	return 0, errors.New("unexpected read")
}

type concurrencyDetectingReader struct {
	active     atomic.Int32
	concurrent atomic.Bool
	sequence   atomic.Uint64
}

func (reader *concurrencyDetectingReader) Read(buffer []byte) (int, error) {
	if active := reader.active.Add(1); active != 1 {
		reader.concurrent.Store(true)
	}
	defer reader.active.Add(-1)
	for range 32 {
		runtime.Gosched()
	}
	for index := range buffer {
		buffer[index] = 0
	}
	sequence := reader.sequence.Add(1)
	binary.BigEndian.PutUint64(buffer[len(buffer)-8:], sequence)
	return len(buffer), nil
}
