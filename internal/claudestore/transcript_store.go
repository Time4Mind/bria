// Package claudestore reads Claude Code's provider-owned transcript store
// without starting, resuming, or authenticating a Claude process.
package claudestore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrTranscriptUnverifiable = errors.New("claude transcript is unverifiable")

const (
	defaultTranscriptMaxBytes = 64 << 20
	defaultTranscriptLineSize = 4 << 20
	defaultTranscriptRecords  = 100_000
	defaultTranscriptFiles    = 10_000
)

type AcceptedTurnOutcome string

const (
	AcceptedTurnCompleted AcceptedTurnOutcome = "completed"
	AcceptedTurnUnknown   AcceptedTurnOutcome = "unknown"
)

type AcceptedTurn struct {
	MessageID string
	Outcome   AcceptedTurnOutcome
}

type SessionSummary struct {
	ID        string
	Cwd       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type TranscriptStoreOptions struct {
	MaxTranscriptBytes int64
	MaxLineBytes       int
	MaxRecords         int
	MaxTranscripts     int
}

// TranscriptStore reads Claude Code's provider-owned project transcripts.
// It exposes only structural identity and timestamps; prompt and response
// content are neither returned nor included in errors.
type TranscriptStore struct {
	root               string
	identity           os.FileInfo
	maxTranscriptBytes int64
	maxLineBytes       int
	maxRecords         int
	maxTranscripts     int
}

func NewTranscriptStore(root string, options TranscriptStoreOptions) (*TranscriptStore, error) {
	if !filepath.IsAbs(root) || strings.ContainsRune(root, '\x00') {
		return nil, ErrTranscriptUnverifiable
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(resolved) {
		return nil, ErrTranscriptUnverifiable
	}
	identity, err := os.Stat(resolved)
	if err != nil || !identity.IsDir() {
		return nil, ErrTranscriptUnverifiable
	}
	maxBytes := options.MaxTranscriptBytes
	if maxBytes == 0 {
		maxBytes = defaultTranscriptMaxBytes
	}
	maxLine := options.MaxLineBytes
	if maxLine == 0 {
		maxLine = defaultTranscriptLineSize
	}
	maxRecords := options.MaxRecords
	if maxRecords == 0 {
		maxRecords = defaultTranscriptRecords
	}
	maxTranscripts := options.MaxTranscripts
	if maxTranscripts == 0 {
		maxTranscripts = defaultTranscriptFiles
	}
	if maxBytes < 1 || maxBytes > 1<<30 || maxLine < 128 || int64(maxLine) > maxBytes ||
		maxRecords < 1 || maxRecords > 1_000_000 || maxTranscripts < 1 || maxTranscripts > 100_000 {
		return nil, ErrTranscriptUnverifiable
	}
	return &TranscriptStore{
		root: resolved, identity: identity, maxTranscriptBytes: maxBytes,
		maxLineBytes: maxLine, maxRecords: maxRecords, maxTranscripts: maxTranscripts,
	}, nil
}

// AcceptedTurns returns exact persisted stream-json uuids from one original
// Claude session. A turn is completed only when an end_turn assistant record
// is linked to that uuid through the transcript parent graph. Every other
// accepted uuid remains unknown and must not be submitted automatically.
func (store *TranscriptStore) AcceptedTurns(ctx context.Context, sessionID, cwd string) ([]AcceptedTurn, error) {
	if ctx == nil || store == nil || !isCanonicalUUID(sessionID) || !safeTranscriptText(cwd, 16<<10) || !filepath.IsAbs(cwd) {
		return nil, ErrTranscriptUnverifiable
	}
	path, err := store.findExactTranscript(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	transcript, err := store.readTranscript(ctx, path, sessionID)
	if err != nil || transcript.cwd != cwd {
		return nil, ErrTranscriptUnverifiable
	}
	turns := make([]acceptedCandidate, 0)
	for index, record := range transcript.records {
		if record.isSDKPrompt() {
			if record.Cwd != cwd || !validUserMessageID(record.UUID) || record.at.IsZero() {
				return nil, ErrTranscriptUnverifiable
			}
			turns = append(turns, acceptedCandidate{messageID: record.UUID, at: record.at, index: index})
		}
	}
	completed, err := completedPromptIDs(transcript.records, transcript.byUUID)
	if err != nil {
		return nil, ErrTranscriptUnverifiable
	}
	sort.SliceStable(turns, func(left, right int) bool {
		if !turns[left].at.Equal(turns[right].at) {
			return turns[left].at.Before(turns[right].at)
		}
		return turns[left].index < turns[right].index
	})
	result := make([]AcceptedTurn, len(turns))
	for index, turn := range turns {
		outcome := AcceptedTurnUnknown
		if completed[turn.messageID] {
			outcome = AcceptedTurnCompleted
		}
		result[index] = AcceptedTurn{MessageID: turn.messageID, Outcome: outcome}
	}
	return result, nil
}

// List returns a deterministic valid prefix and a sanitized error if a
// canonical transcript cannot be structurally verified.
func (store *TranscriptStore) List(ctx context.Context) ([]SessionSummary, error) {
	paths, err := store.transcriptPaths(ctx, "")
	if err != nil {
		return nil, err
	}
	result := make([]SessionSummary, 0, len(paths))
	seenSessionIDs := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if _, duplicate := seenSessionIDs[sessionID]; duplicate {
			return result, ErrTranscriptUnverifiable
		}
		seenSessionIDs[sessionID] = struct{}{}
		transcript, readErr := store.readTranscript(ctx, path, sessionID)
		if readErr != nil {
			return result, ErrTranscriptUnverifiable
		}
		result = append(result, SessionSummary{
			ID: sessionID, Cwd: transcript.cwd,
			CreatedAt: transcript.createdAt, UpdatedAt: transcript.updatedAt,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if !result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].UpdatedAt.After(result[right].UpdatedAt)
		}
		return result[left].ID < result[right].ID
	})
	return result, nil
}

type acceptedCandidate struct {
	messageID string
	at        time.Time
	index     int
}

type parsedTranscript struct {
	records   []transcriptRecord
	byUUID    map[string]int
	cwd       string
	createdAt time.Time
	updatedAt time.Time
}

type transcriptRecord struct {
	Type         string `json:"type"`
	UserType     string `json:"userType"`
	PromptSource string `json:"promptSource"`
	SessionID    string `json:"sessionId"`
	Cwd          string `json:"cwd"`
	Timestamp    string `json:"timestamp"`
	UUID         string `json:"uuid"`
	ParentUUID   string `json:"parentUuid"`
	Message      struct {
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
	} `json:"message"`
	at time.Time
}

func (record transcriptRecord) isSDKPrompt() bool {
	return record.Type == "user" && record.UserType == "external" && record.PromptSource == "sdk" && record.Message.Role == "user"
}

func validUserMessageID(messageID string) bool {
	return safeTranscriptText(messageID, 512)
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 || value != strings.ToLower(value) ||
		value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16
}

func (store *TranscriptStore) findExactTranscript(ctx context.Context, sessionID string) (string, error) {
	paths, err := store.transcriptPaths(ctx, sessionID)
	if err != nil || len(paths) != 1 {
		return "", ErrTranscriptUnverifiable
	}
	return paths[0], nil
}

func (store *TranscriptStore) transcriptPaths(ctx context.Context, exactSessionID string) ([]string, error) {
	if err := store.verifyRoot(); err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(store.root)
	if err != nil {
		return nil, ErrTranscriptUnverifiable
	}
	result := make([]string, 0)
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if project.Type()&os.ModeSymlink != 0 || !project.IsDir() {
			continue
		}
		projectPath := filepath.Join(store.root, project.Name())
		files, readErr := os.ReadDir(projectPath)
		if readErr != nil {
			return nil, ErrTranscriptUnverifiable
		}
		for _, file := range files {
			if file.Type()&os.ModeSymlink != 0 || file.IsDir() || filepath.Ext(file.Name()) != ".jsonl" {
				continue
			}
			sessionID := strings.TrimSuffix(file.Name(), ".jsonl")
			if !isCanonicalUUID(sessionID) || exactSessionID != "" && sessionID != exactSessionID {
				continue
			}
			result = append(result, filepath.Join(projectPath, file.Name()))
			if len(result) > store.maxTranscripts {
				return nil, ErrTranscriptUnverifiable
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func (store *TranscriptStore) verifyRoot() error {
	if store == nil || store.identity == nil {
		return ErrTranscriptUnverifiable
	}
	current, err := os.Stat(store.root)
	if err != nil || !current.IsDir() || !os.SameFile(current, store.identity) {
		return ErrTranscriptUnverifiable
	}
	return nil
}

func (store *TranscriptStore) readTranscript(ctx context.Context, path, sessionID string) (parsedTranscript, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > store.maxTranscriptBytes {
		return parsedTranscript{}, ErrTranscriptUnverifiable
	}
	file, err := os.Open(path)
	if err != nil {
		return parsedTranscript{}, ErrTranscriptUnverifiable
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() {
		return parsedTranscript{}, ErrTranscriptUnverifiable
	}

	parsed := parsedTranscript{records: make([]transcriptRecord, 0), byUUID: make(map[string]int)}
	scanner := bufio.NewScanner(io.LimitReader(file, store.maxTranscriptBytes+1))
	scanner.Buffer(make([]byte, min(store.maxLineBytes, 4096)), store.maxLineBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return parsedTranscript{}, err
		}
		if len(parsed.records) >= store.maxRecords {
			return parsedTranscript{}, ErrTranscriptUnverifiable
		}
		record, decodeErr := decodeTranscriptRecord(scanner.Bytes())
		if decodeErr != nil || record.SessionID != sessionID {
			return parsedTranscript{}, ErrTranscriptUnverifiable
		}
		if record.Timestamp != "" {
			record.at, err = time.Parse(time.RFC3339Nano, record.Timestamp)
			if err != nil {
				return parsedTranscript{}, ErrTranscriptUnverifiable
			}
			record.at = record.at.UTC()
			if parsed.createdAt.IsZero() || record.at.Before(parsed.createdAt) {
				parsed.createdAt = record.at
			}
			if parsed.updatedAt.IsZero() || record.at.After(parsed.updatedAt) {
				parsed.updatedAt = record.at
			}
		}
		if record.Cwd != "" {
			if !safeTranscriptText(record.Cwd, 16<<10) || !filepath.IsAbs(record.Cwd) || parsed.cwd != "" && parsed.cwd != record.Cwd {
				return parsedTranscript{}, ErrTranscriptUnverifiable
			}
			parsed.cwd = record.Cwd
		}
		if record.UUID != "" {
			if !validUserMessageID(record.UUID) {
				return parsedTranscript{}, ErrTranscriptUnverifiable
			}
			if _, duplicate := parsed.byUUID[record.UUID]; duplicate {
				return parsedTranscript{}, ErrTranscriptUnverifiable
			}
			parsed.byUUID[record.UUID] = len(parsed.records)
		}
		if record.ParentUUID != "" && !validUserMessageID(record.ParentUUID) {
			return parsedTranscript{}, ErrTranscriptUnverifiable
		}
		parsed.records = append(parsed.records, record)
	}
	if scanner.Err() != nil || len(parsed.records) == 0 || parsed.cwd == "" || parsed.createdAt.IsZero() || parsed.updatedAt.IsZero() {
		return parsedTranscript{}, ErrTranscriptUnverifiable
	}
	final, err := file.Stat()
	if err != nil || final.Size() != before.Size() || !os.SameFile(before, final) {
		return parsedTranscript{}, ErrTranscriptUnverifiable
	}
	return parsed, nil
}

func completedPromptIDs(records []transcriptRecord, byUUID map[string]int) (map[string]bool, error) {
	completed := make(map[string]bool)
	for index, record := range records {
		if record.Type != "assistant" || record.Message.Role != "assistant" || record.Message.StopReason != "end_turn" {
			continue
		}
		if record.UUID == "" || record.at.IsZero() {
			return nil, ErrTranscriptUnverifiable
		}
		seen := make(map[string]struct{})
		parent := record.ParentUUID
		childTimestamp := record.at
		for parent != "" {
			if _, duplicate := seen[parent]; duplicate {
				return nil, ErrTranscriptUnverifiable
			}
			seen[parent] = struct{}{}
			parentIndex, exists := byUUID[parent]
			if !exists {
				break
			}
			if parentIndex == index {
				return nil, ErrTranscriptUnverifiable
			}
			ancestor := records[parentIndex]
			if ancestor.at.IsZero() || ancestor.at.After(childTimestamp) {
				return nil, ErrTranscriptUnverifiable
			}
			if ancestor.isSDKPrompt() {
				completed[ancestor.UUID] = true
				break
			}
			parent = ancestor.ParentUUID
			childTimestamp = ancestor.at
		}
	}
	return completed, nil
}

func decodeTranscriptRecord(line []byte) (transcriptRecord, error) {
	if len(line) == 0 || !utf8.Valid(line) || rejectDuplicateJSONKeys(line) != nil {
		return transcriptRecord{}, ErrTranscriptUnverifiable
	}
	var record transcriptRecord
	if err := json.Unmarshal(line, &record); err != nil || record.Type == "" || record.SessionID == "" {
		return transcriptRecord{}, ErrTranscriptUnverifiable
	}
	return record, nil
}

func rejectDuplicateJSONKeys(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	if err := scanTranscriptJSON(decoder); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrTranscriptUnverifiable
	}
	return nil
}

func scanTranscriptJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return ErrTranscriptUnverifiable
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, ok := keyToken.(string)
			if keyErr != nil || !ok {
				return ErrTranscriptUnverifiable
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrTranscriptUnverifiable
			}
			seen[key] = struct{}{}
			if err := scanTranscriptJSON(decoder); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return ErrTranscriptUnverifiable
		}
	case '[':
		for decoder.More() {
			if err := scanTranscriptJSON(decoder); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return ErrTranscriptUnverifiable
		}
	default:
		return ErrTranscriptUnverifiable
	}
	return nil
}

func safeTranscriptText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
