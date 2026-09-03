// Package safelog provides the only boundary through which structured local
// diagnostic events should be persisted. It sanitizes records in memory before
// any bytes are handed to the filesystem.
package safelog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Class string

const (
	Detailed Class = "detailed"
	Service  Class = "service"
	Critical Class = "critical"
)

const (
	detailedRetention = 6 * time.Hour
	serviceRetention  = 24 * time.Hour
	criticalRetention = 72 * time.Hour
)

var (
	ErrInvalidOptions = errors.New("safelog: invalid options")
	ErrInvalidEvent   = errors.New("safelog: invalid event")
	ErrInvalidClass   = errors.New("safelog: invalid class")
	ErrRecordTooLarge = errors.New("safelog: record too large")
	ErrCorruptLog     = errors.New("safelog: corrupt log")
	ErrStorage        = errors.New("safelog: storage operation failed")
)

type Options struct {
	Directory      string
	MaxRecords     int
	MaxRecordBytes int64
	MaxFileBytes   int64
	Now            func() time.Time
}

type Event struct {
	Class         Class             `json:"class"`
	Type          string            `json:"type"`
	EntityID      string            `json:"entity_id,omitempty"`
	Time          time.Time         `json:"time"`
	Result        string            `json:"result,omitempty"`
	ErrorCategory string            `json:"error_category,omitempty"`
	Error         string            `json:"error,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
}

type Logger struct {
	directory      string
	maxRecords     int
	maxRecordBytes int64
	maxFileBytes   int64
	now            func() time.Time
	lockPath       string
	mu             *sync.Mutex
}

const (
	defaultMaxRecords     = 10_000
	defaultMaxRecordBytes = 64 << 10
	defaultMaxFileBytes   = 8 << 20
)

var directoryLocks sync.Map

func Open(options Options) (*Logger, error) {
	if options.Directory == "" || !filepath.IsAbs(options.Directory) {
		return nil, ErrInvalidOptions
	}
	if options.MaxRecords < 0 || options.MaxRecordBytes < 0 || options.MaxFileBytes < 0 {
		return nil, ErrInvalidOptions
	}
	if options.MaxRecords == 0 {
		options.MaxRecords = defaultMaxRecords
	}
	if options.MaxRecordBytes == 0 {
		options.MaxRecordBytes = defaultMaxRecordBytes
	}
	if options.MaxFileBytes == 0 {
		options.MaxFileBytes = defaultMaxFileBytes
	}
	if options.MaxFileBytes < options.MaxRecordBytes {
		return nil, ErrInvalidOptions
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	directory := filepath.Clean(options.Directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, ErrStorage
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, ErrStorage
	}
	shared, _ := directoryLocks.LoadOrStore(directory, &sync.Mutex{})
	return &Logger{
		directory:      directory,
		maxRecords:     options.MaxRecords,
		maxRecordBytes: options.MaxRecordBytes,
		maxFileBytes:   options.MaxFileBytes,
		now:            options.Now,
		lockPath:       filepath.Join(directory, ".lock"),
		mu:             shared.(*sync.Mutex),
	}, nil
}

// Write sanitizes event before it is encoded or passed to filesystem APIs.
func (logger *Logger) Write(event Event) error {
	if logger == nil {
		return ErrInvalidOptions
	}
	if !event.Class.valid() {
		return ErrInvalidClass
	}
	if !safeEventNamePattern.MatchString(event.Type) {
		return ErrInvalidEvent
	}
	// Reject oversized caller input before redaction. This keeps the persistent
	// bound meaningful even when a large sensitive value collapses to one marker;
	// the encoded bytes are held in memory and are never passed to filesystem APIs.
	original, err := encodeEvent(event)
	if err != nil {
		return err
	}
	if int64(len(original)) > logger.maxRecordBytes || int64(len(original)) > logger.maxFileBytes {
		return ErrRecordTooLarge
	}
	now := logger.now().UTC()
	if now.IsZero() {
		return ErrInvalidOptions
	}
	// Retention is measured from the write, not from caller-controlled data.
	event.Time = now
	event = sanitizeEvent(event)
	encoded, err := encodeEvent(event)
	if err != nil {
		return err
	}
	if int64(len(encoded)) > logger.maxRecordBytes || int64(len(encoded)) > logger.maxFileBytes {
		return ErrRecordTooLarge
	}
	return logger.withLock(func() error {
		records, err := logger.readUnlocked(event.Class)
		if err != nil {
			return err
		}
		records = retain(records, now, event.Class.retention())
		records = append(records, event)
		records, err = logger.fit(records)
		if err != nil {
			return err
		}
		return logger.writeUnlocked(event.Class, records)
	})
}

// Cleanup removes expired records from every retention class. Each class file
// is replaced atomically while the shared interprocess lock is held.
func (logger *Logger) Cleanup() error {
	if logger == nil {
		return ErrInvalidOptions
	}
	return logger.withLock(func() error {
		now := logger.now().UTC()
		for _, class := range []Class{Detailed, Service, Critical} {
			records, err := logger.readUnlocked(class)
			if err != nil {
				return err
			}
			kept := retain(records, now, class.retention())
			if len(kept) == len(records) {
				continue
			}
			if err := logger.writeUnlocked(class, kept); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunCleanup performs an immediate cleanup and repeats it until ctx ends. It is
// intended to be supervised with the other long-running local services, so
// expiry does not depend on new log traffic being produced.
func (logger *Logger) RunCleanup(ctx context.Context, interval time.Duration) error {
	if logger == nil || ctx == nil || interval <= 0 {
		return ErrInvalidOptions
	}
	if err := logger.Cleanup(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := logger.Cleanup(); err != nil {
				return err
			}
		}
	}
}

// Read returns already-sanitized persisted records in chronological append order.
func (logger *Logger) Read(class Class) ([]Event, error) {
	if logger == nil {
		return nil, ErrInvalidOptions
	}
	if !class.valid() {
		return nil, ErrInvalidClass
	}
	var result []Event
	err := logger.withLock(func() error {
		var err error
		result, err = logger.readUnlocked(class)
		return err
	})
	return result, err
}

func (logger *Logger) fit(records []Event) ([]Event, error) {
	if len(records) > logger.maxRecords {
		records = records[len(records)-logger.maxRecords:]
	}
	for len(records) > 0 {
		encoded, err := encodeRecords(records)
		if err != nil {
			return nil, err
		}
		if int64(len(encoded)) <= logger.maxFileBytes {
			return records, nil
		}
		records = records[1:]
	}
	return records, nil
}

func (logger *Logger) withLock(operation func() error) error {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	lock, err := acquireFileLock(logger.lockPath)
	if err != nil {
		return ErrStorage
	}
	defer lock.release()
	return operation()
}

func (logger *Logger) readUnlocked(class Class) ([]Event, error) {
	file, err := os.Open(logger.path(class))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	defer file.Close()
	reader := io.LimitReader(file, logger.maxFileBytes+1)
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, ErrStorage
	}
	if int64(len(raw)) > logger.maxFileBytes {
		return nil, ErrCorruptLog
	}
	var records []Event
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), int(logger.maxRecordBytes)+1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if int64(len(line)) > logger.maxRecordBytes {
			return nil, ErrCorruptLog
		}
		var event Event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil || !event.Class.valid() || event.Class != class || event.Type == "" || event.Time.IsZero() {
			return nil, ErrCorruptLog
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return nil, ErrCorruptLog
		}
		records = append(records, event)
		if len(records) > logger.maxRecords {
			return nil, ErrCorruptLog
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, ErrCorruptLog
	}
	return records, nil
}

func (logger *Logger) writeUnlocked(class Class, records []Event) (returnErr error) {
	data, err := encodeRecords(records)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(logger.directory, ".safelog-*")
	if err != nil {
		return ErrStorage
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return ErrStorage
	}
	if _, err := temporary.Write(data); err != nil {
		return ErrStorage
	}
	if err := temporary.Sync(); err != nil {
		return ErrStorage
	}
	if err := temporary.Close(); err != nil {
		return ErrStorage
	}
	if err := os.Rename(temporaryPath, logger.path(class)); err != nil {
		return ErrStorage
	}
	if err := syncDirectory(logger.directory); err != nil {
		return ErrStorage
	}
	// A successful write means the final path itself exactly matches the intended
	// bytes and was independently decoded under the configured limits.
	raw, err := os.ReadFile(logger.path(class))
	if err != nil || !bytes.Equal(raw, data) {
		return ErrStorage
	}
	persisted, err := logger.readUnlocked(class)
	if err != nil || len(persisted) != len(records) {
		return ErrStorage
	}
	return nil
}

func encodeEvent(event Event) ([]byte, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, ErrInvalidEvent
	}
	encoded = append(encoded, '\n')
	return encoded, nil
}

func encodeRecords(records []Event) ([]byte, error) {
	var buffer bytes.Buffer
	for _, event := range records {
		encoded, err := encodeEvent(event)
		if err != nil {
			return nil, err
		}
		buffer.Write(encoded)
	}
	return buffer.Bytes(), nil
}

func retain(records []Event, now time.Time, retention time.Duration) []Event {
	kept := make([]Event, 0, len(records))
	for _, event := range records {
		if event.Time.After(now) || now.Sub(event.Time) < retention {
			kept = append(kept, event)
		}
	}
	return kept
}

func (logger *Logger) path(class Class) string {
	return filepath.Join(logger.directory, string(class)+".jsonl")
}

func (class Class) valid() bool {
	return class == Detailed || class == Service || class == Critical
}

func (class Class) retention() time.Duration {
	switch class {
	case Detailed:
		return detailedRetention
	case Service:
		return serviceRetention
	case Critical:
		return criticalRetention
	default:
		return 0
	}
}

var (
	safeEventNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	safeFieldNamePattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	safeFieldValuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@+-]{0,255}$`)
	telegramURLPattern    = regexp.MustCompile(`(?i)https://api\.telegram\.org/bot[^\s/]+(?:/[^\s"']*)?`)
	telegramTokenPattern  = regexp.MustCompile(`\b[0-9]{5,}:[A-Za-z0-9_-]{10,}\b`)
	authorizationPattern  = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(?:bearer|basic|oauth)?\s*[^\s,;]+`)
	querySecretPattern    = regexp.MustCompile(`(?i)(token|secret|password|authorization|access_token|auth_code|callback_code|pairing_code|code)=([^&\s]+)`)
	fileURLPattern        = regexp.MustCompile(`(?i)file://[^\s"']+`)
	windowsPathPattern    = regexp.MustCompile(`[A-Za-z]:\\(?:[^\s"']+\\?)+`)
	unixPathPattern       = regexp.MustCompile(`(^|[\s="'(])/(?:[^\s"'(),;]+/?)+`)
)

func sanitizeEvent(event Event) Event {
	event.EntityID = sanitizeLabel(event.EntityID)
	event.Result = sanitizeLabel(event.Result)
	event.ErrorCategory = sanitizeLabel(event.ErrorCategory)
	if event.Error != "" {
		// Free-form errors may contain arbitrary user or agent text that pattern
		// matching cannot identify reliably. ErrorCategory is the safe diagnostic
		// contract; the unstructured value is therefore fail-closed.
		event.Error = "[REDACTED]"
	}
	if len(event.Fields) > 0 {
		keys := make([]string, 0, len(event.Fields))
		for key := range event.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		sanitized := make(map[string]string, len(event.Fields))
		redactedField := 0
		for _, key := range keys {
			safeKey := key
			knownSafeField := safeStructuredField(key)
			if !knownSafeField || !safeFieldNamePattern.MatchString(key) {
				redactedField++
				safeKey = "redacted_field_" + strconv.Itoa(redactedField)
			}
			if (!numericStructuredField(key) && sensitiveKey(key)) || !knownSafeField {
				sanitized[safeKey] = "[REDACTED]"
			} else if numericStructuredField(key) && !safeNonNegativeDecimal(event.Fields[key]) {
				// Timing and load counters are numeric by contract. Do not preserve
				// signed, decimal, or overflowing caller values as labels.
				sanitized[safeKey] = "[REDACTED]"
			} else {
				sanitized[safeKey] = sanitizeLabel(event.Fields[key])
			}
		}
		event.Fields = sanitized
	}
	return event
}

func sanitizeLabel(value string) string {
	if value == "" {
		return ""
	}
	value = sanitizeText(value)
	if !safeFieldValuePattern.MatchString(value) {
		return "[REDACTED]"
	}
	return value
}

// safeStructuredField is deliberately an allowlist. Unknown fields fail closed
// because their values could be Telegram messages, agent output or file content.
func safeStructuredField(key string) bool {
	switch strings.ToLower(key) {
	case "action", "arch", "attempt", "component", "computer_id", "count",
		"duration_ms", "entity_kind", "exit_code", "generation", "operation",
		"os", "previous_state", "provider", "queue_depth", "result", "revision",
		"session_id", "signal", "state", "status", "term", "version",
		"queue_wait_ms", "provider_accept_ms", "first_event_ms", "total_ms",
		"http_to_headers_ms", "http_total_ms", "retry_delay_ms", "bytes",
		"oldest_age_ms", "active_turns", "unknown_count", "failed_count":
		return true
	default:
		return false
	}
}

func numericStructuredField(key string) bool {
	switch strings.ToLower(key) {
	case "duration_ms", "queue_wait_ms", "provider_accept_ms", "first_event_ms", "total_ms",
		"http_to_headers_ms", "http_total_ms", "retry_delay_ms", "bytes",
		"oldest_age_ms", "active_turns", "unknown_count", "failed_count":
		return true
	default:
		return false
	}
}

func safeNonNegativeDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(value, 10, 63)
	return err == nil
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(key))
	for _, fragment := range []string{
		"token", "secret", "password", "authorization", "credential", "cookie",
		"auth_code", "callback", "pairing_code", "telegram_message", "message",
		"prompt", "response", "content", "caption", "photo", "body", "path",
		"filename", "file_name", "auth_file", "url", "uri", "endpoint", "headers",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return normalized == "code"
}

func sanitizeText(value string) string {
	if value == "" {
		return ""
	}
	value = telegramURLPattern.ReplaceAllString(value, "[REDACTED_TELEGRAM_URL]")
	value = telegramTokenPattern.ReplaceAllString(value, "[REDACTED]")
	value = authorizationPattern.ReplaceAllString(value, "${1}[REDACTED]")
	value = querySecretPattern.ReplaceAllString(value, "${1}=[REDACTED]")
	value = fileURLPattern.ReplaceAllString(value, "[REDACTED_PATH]")
	value = windowsPathPattern.ReplaceAllString(value, "[REDACTED_PATH]")
	value = unixPathPattern.ReplaceAllString(value, "${1}[REDACTED_PATH]")
	return value
}
