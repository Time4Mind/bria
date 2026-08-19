package transcript

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/processlog"
)

var providerSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type Reader struct {
	config       Config
	resolveMu    sync.Mutex
	resolveCache map[resolveCacheKey]resolveCacheEntry
	readMu       sync.Mutex
	readCache    map[string]readCacheEntry
	readOrder    []string
	readFlights  map[string]chan struct{}
}

type resolveCacheKey struct {
	backend   Backend
	sessionID string
	workdir   string
}

type resolveCacheEntry struct {
	path      string
	expiresAt time.Time
}

type readCacheEntry struct {
	size       int64
	modifiedAt time.Time
	info       os.FileInfo
	events     []Event
}

const negativeResolveTTL = time.Second
const maxReadCacheEntries = 32
const initialParseLines = 256

func NewReader(config Config) (*Reader, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &Reader{
		config:       normalized,
		resolveCache: make(map[resolveCacheKey]resolveCacheEntry),
		readCache:    make(map[string]readCacheEntry),
		readFlights:  make(map[string]chan struct{}),
	}, nil
}

func (r *Reader) Read(ctx context.Context, request Request) ([]Event, error) {
	startedAt := time.Now()
	cacheHit := false
	readMode := "full"
	eventCount := 0
	lineCount := 0
	parsedLineCount := 0
	defer func() {
		processlog.Detailf(
			"bria transcript: read_timing backend=%s total_ms=%d cache=%t "+
				"mode=%s lines=%d parsed_lines=%d events=%d",
			request.Backend, time.Since(startedAt).Milliseconds(), cacheHit,
			readMode, lineCount, parsedLineCount, eventCount,
		)
	}()
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	path, err := r.resolveTranscriptPath(ctx, request)
	if err != nil {
		return nil, err
	}
	var before os.FileInfo
	for {
		before, err = os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat transcript: %w", err)
		}
		if events, ok := r.cachedRead(path, before); ok {
			cacheHit = true
			readMode = "cache"
			eventCount = len(events)
			return events, nil
		}
		wait, owner := r.claimRead(path)
		if owner {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
	defer r.finishRead(path)
	if events, lines, parsed, ok, appendErr := r.readAppended(
		path, before, request.Backend,
	); appendErr != nil {
		return nil, appendErr
	} else if ok {
		readMode = "append"
		lineCount = lines
		parsedLineCount = parsed
		eventCount = len(events)
		if after, statErr := os.Stat(path); statErr == nil && sameTranscriptFileVersion(before, after) {
			r.rememberRead(path, after, events)
		}
		return events, nil
	}

	lines, err := readRecentJSONLLines(path, r.config.MaxReadBytes, r.config.MaxLineBytes)
	if err != nil {
		return nil, err
	}
	lineCount = len(lines)
	events, parsedLineCount := parseRecentEvents(
		request.Backend, lines, r.config.MaxBodyBytes, r.config.MaxEvents,
	)
	eventCount = len(events)
	if after, statErr := os.Stat(path); statErr == nil && sameTranscriptFileVersion(before, after) {
		r.rememberRead(path, after, events)
	}
	return events, nil
}

// ReadFirstUserText scans from the start of the JSONL instead of using the
// recent-event window. This is intentionally separate from Read: session
// naming must use the actual first prompt even when a transcript exceeds the
// card reader's bounded suffix.
func (r *Reader) ReadFirstUserText(ctx context.Context, request Request) (string, error) {
	if err := validateRequest(request); err != nil {
		return "", err
	}
	path, err := r.resolveTranscriptPath(ctx, request)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, min(r.config.MaxLineBytes, 64<<10))
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		line, oversized, readErr := readBoundedLine(reader, r.config.MaxLineBytes)
		if !oversized {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				var events []Event
				switch request.Backend {
				case BackendClaude:
					events = parseClaude([][]byte{line}, r.config.MaxBodyBytes)
				case BackendCodex:
					events = parseCodex([][]byte{line}, r.config.MaxBodyBytes)
				}
				for _, event := range events {
					if event.Kind == EventUserText && strings.TrimSpace(event.Text) != "" {
						return event.Text, nil
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return "", nil
			}
			return "", fmt.Errorf("read transcript: %w", readErr)
		}
	}
}

func (r *Reader) resolveTranscriptPath(ctx context.Context, request Request) (string, error) {
	switch request.Backend {
	case BackendClaude:
		return r.resolveClaude(request)
	case BackendCodex:
		return r.resolveCodex(ctx, request)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedBackend, request.Backend)
	}
}

func validateRequest(request Request) error {
	if !providerSessionIDPattern.MatchString(request.ProviderSessionID) {
		return fmt.Errorf("%w: invalid provider session id", ErrInvalidRequest)
	}
	if !filepath.IsAbs(request.Workdir) {
		return fmt.Errorf("%w: workdir must be absolute", ErrInvalidRequest)
	}
	if strings.ContainsRune(request.Workdir, 0) {
		return fmt.Errorf("%w: workdir contains NUL", ErrInvalidRequest)
	}
	return nil
}

func safeRegularFile(root, candidate string) (string, error) {
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve transcript root: %w", err)
	}
	rootPath, err = filepath.EvalSymlinks(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrTranscriptNotFound
		}
		return "", fmt.Errorf("resolve transcript root: %w", err)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrTranscriptNotFound
		}
		return "", fmt.Errorf("inspect transcript: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", ErrUnsafeTranscript
	}
	candidatePath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve transcript: %w", err)
	}
	relative, err := filepath.Rel(rootPath, candidatePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeTranscript
	}
	return candidatePath, nil
}

func bounded(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
