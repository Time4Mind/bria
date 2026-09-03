// Package screen owns a bounded virtual terminal derived only from typed
// provider runtime events. It never captures the operating-system desktop,
// reads a PTY, or accepts raw prompt, authorization, or secret input.
package screen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

var (
	ErrInvalidConfiguration = errors.New("screen configuration is invalid")
	ErrInvalidSession       = errors.New("screen session identity is invalid")
	ErrUnknownSession       = errors.New("screen session does not exist")
	ErrSessionLimit         = errors.New("screen session limit reached")
	ErrInvalidEvent         = errors.New("screen typed runtime event is invalid")
	ErrEventTooLarge        = errors.New("screen typed runtime event exceeds its bound")
	ErrSnapshotTooLarge     = errors.New("screen PNG exceeds its bound")
)

const (
	maxConfiguredSessions = 256
	maxConfiguredLines    = 200
	maxConfiguredColumns  = 240
	maxConfiguredEvent    = 64 << 10
	maxConfiguredPNG      = 10 << 20
	glyphWidth            = 5
	glyphHeight           = 7
	cellWidth             = 6
	cellHeight            = 8
	imagePadding          = 4
)

type Options struct {
	MaxSessions   int
	MaxLines      int
	MaxColumns    int
	MaxEventBytes int
	MaxPNGBytes   int
}

type terminal struct {
	revision uint64
	lines    []string
}

// Store keeps independent bounded virtual terminals. The caller owns the one
// global Screen-enabled preference and decides when Snapshot is requested.
type Store struct {
	options  Options
	mu       sync.RWMutex
	sessions map[domain.SessionID]*terminal
}

type EventAdapter struct {
	store     *Store
	sessionID domain.SessionID
}

type Snapshot struct {
	SessionID domain.SessionID
	Revision  uint64
	Lines     []string
	PNG       []byte
	Width     int
	Height    int
}

// TelegramMedia is transport-neutral immutable content for a Telegram photo
// adapter. Chat identity and external delivery receipts stay with composition.
type TelegramMedia struct {
	FileName    string
	ContentType string
	Content     []byte
}

type MediaReceipt struct {
	MessageID int64
}

type TelegramMediaSender interface {
	SendScreen(context.Context, domain.SessionID, TelegramMedia) (MediaReceipt, error)
}

func New(options Options) (*Store, error) {
	if options.MaxSessions <= 0 || options.MaxSessions > maxConfiguredSessions ||
		options.MaxLines <= 0 || options.MaxLines > maxConfiguredLines ||
		options.MaxColumns <= 0 || options.MaxColumns > maxConfiguredColumns ||
		options.MaxEventBytes <= 0 || options.MaxEventBytes > maxConfiguredEvent ||
		options.MaxPNGBytes <= 0 || options.MaxPNGBytes > maxConfiguredPNG {
		return nil, ErrInvalidConfiguration
	}
	return &Store{options: options, sessions: make(map[domain.SessionID]*terminal)}, nil
}

// Events returns the exact typed-event adapter for one logical session.
func (store *Store) Events(sessionID domain.SessionID) (EventAdapter, error) {
	if store == nil || !validSessionID(sessionID) {
		return EventAdapter{}, ErrInvalidSession
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, found := store.sessions[sessionID]; !found {
		if len(store.sessions) >= store.options.MaxSessions {
			return EventAdapter{}, ErrSessionLimit
		}
		store.sessions[sessionID] = &terminal{lines: make([]string, 0, store.options.MaxLines)}
	}
	return EventAdapter{store: store, sessionID: sessionID}, nil
}

// Remove releases a virtual-terminal slot when its logical session no longer
// needs live Screen state.
func (store *Store) Remove(ctx context.Context, sessionID domain.SessionID) error {
	if store == nil || ctx == nil || !validSessionID(sessionID) {
		return ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, found := store.sessions[sessionID]; !found {
		return ErrUnknownSession
	}
	delete(store.sessions, sessionID)
	return nil
}

// Callback fits sessionruntime.TurnCallbacks.OnEvent without exposing any API
// for raw terminal stdin, prompts, auth codes, or provider protocol bytes.
func (adapter EventAdapter) Callback(ctx context.Context) func(sessionruntime.TurnEvent) error {
	return func(event sessionruntime.TurnEvent) error { return adapter.Handle(ctx, event) }
}

func (adapter EventAdapter) Handle(ctx context.Context, event sessionruntime.TurnEvent) error {
	if adapter.store == nil || !validSessionID(adapter.sessionID) || ctx == nil {
		return ErrInvalidEvent
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.Kind != sessionruntime.EventCommentary && event.Kind != sessionruntime.EventQuestion {
		return ErrInvalidEvent
	}
	if len(event.Text) == 0 || !utf8.ValidString(event.Text) {
		return ErrInvalidEvent
	}
	if len(event.Text) > adapter.store.options.MaxEventBytes {
		return ErrEventTooLarge
	}
	lines := sanitizeAndWrap(event.Text, adapter.store.options.MaxColumns)
	if len(lines) == 0 {
		return ErrInvalidEvent
	}
	adapter.store.mu.Lock()
	defer adapter.store.mu.Unlock()
	terminal, found := adapter.store.sessions[adapter.sessionID]
	if !found {
		return ErrUnknownSession
	}
	terminal.lines = append(terminal.lines, lines...)
	if excess := len(terminal.lines) - adapter.store.options.MaxLines; excess > 0 {
		copy(terminal.lines, terminal.lines[excess:])
		terminal.lines = terminal.lines[:adapter.store.options.MaxLines]
	}
	terminal.revision++
	return nil
}

// Snapshot atomically copies one revision and then renders that immutable copy.
func (store *Store) Snapshot(ctx context.Context, sessionID domain.SessionID) (Snapshot, error) {
	if store == nil || ctx == nil || !validSessionID(sessionID) {
		return Snapshot{}, ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.mu.RLock()
	terminal, found := store.sessions[sessionID]
	if !found {
		store.mu.RUnlock()
		return Snapshot{}, ErrUnknownSession
	}
	revision := terminal.revision
	lines := append([]string(nil), terminal.lines...)
	options := store.options
	store.mu.RUnlock()

	encoded, width, height, err := renderPNG(lines, options)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		SessionID: sessionID, Revision: revision, Lines: lines,
		PNG: encoded, Width: width, Height: height,
	}, nil
}

func (snapshot Snapshot) TelegramMedia() TelegramMedia {
	digest := sha256.Sum256([]byte(snapshot.SessionID))
	return TelegramMedia{
		FileName:    fmt.Sprintf("bria-screen-%s-%d.png", hex.EncodeToString(digest[:8]), snapshot.Revision),
		ContentType: "image/png", Content: append([]byte(nil), snapshot.PNG...),
	}
}

func validSessionID(sessionID domain.SessionID) bool {
	value := string(sessionID)
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}

func sanitizeAndWrap(value string, maxColumns int) []string {
	plain := stripTerminalControls(value)
	lines := make([]string, 0, strings.Count(plain, "\n")+1)
	for _, raw := range strings.Split(plain, "\n") {
		runes := []rune(raw)
		if len(runes) == 0 {
			lines = append(lines, "")
			continue
		}
		for len(runes) > maxColumns {
			lines = append(lines, string(runes[:maxColumns]))
			runes = runes[maxColumns:]
		}
		lines = append(lines, string(runes))
	}
	return lines
}

func stripTerminalControls(value string) string {
	output := make([]rune, 0, len(value))
	for index := 0; index < len(value); {
		if value[index] == 0x1b {
			index = skipEscape(value, index)
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		index += size
		switch r {
		case '\r':
			output = append(output, '\n')
		case '\n':
			output = append(output, '\n')
		case '\t':
			output = append(output, ' ')
		case '\b':
			if len(output) > 0 && output[len(output)-1] != '\n' {
				output = output[:len(output)-1]
			}
		default:
			if r >= 0x80 && r <= 0x9f || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				continue
			}
			output = append(output, r)
		}
	}
	return string(output)
}

func skipEscape(value string, start int) int {
	if start+1 >= len(value) {
		return len(value)
	}
	next := value[start+1]
	switch next {
	case '[':
		for index := start + 2; index < len(value); index++ {
			if value[index] >= 0x40 && value[index] <= 0x7e {
				return index + 1
			}
		}
		return len(value)
	case ']', 'P', '^', '_':
		for index := start + 2; index < len(value); index++ {
			if value[index] == 0x07 {
				return index + 1
			}
			if value[index] == 0x1b && index+1 < len(value) && value[index+1] == '\\' {
				return index + 2
			}
		}
		return len(value)
	default:
		return min(start+2, len(value))
	}
}

func renderPNG(lines []string, options Options) ([]byte, int, int, error) {
	width := imagePadding*2 + options.MaxColumns*cellWidth
	height := imagePadding*2 + options.MaxLines*cellHeight
	canvas := image.NewGray(image.Rect(0, 0, width, height))
	white := color.Gray{Y: 255}
	black := color.Gray{Y: 0}
	for index := range canvas.Pix {
		canvas.Pix[index] = white.Y
	}
	for row, line := range lines {
		if row >= options.MaxLines {
			break
		}
		for column, character := range []rune(line) {
			if column >= options.MaxColumns {
				break
			}
			drawGlyph(canvas, imagePadding+column*cellWidth, imagePadding+row*cellHeight, character, black)
		}
	}
	buffer := &boundedBuffer{limit: options.MaxPNGBytes}
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(buffer, canvas); err != nil {
		if errors.Is(err, ErrSnapshotTooLarge) || buffer.exceeded {
			return nil, 0, 0, ErrSnapshotTooLarge
		}
		return nil, 0, 0, errors.New("encode screen PNG")
	}
	return append([]byte(nil), buffer.buffer.Bytes()...), width, height, nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if len(value) <= remaining {
		return buffer.buffer.Write(value)
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(value[:remaining])
	}
	buffer.exceeded = true
	return max(remaining, 0), ErrSnapshotTooLarge
}
