package transcript

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var providerSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type Reader struct {
	config       Config
	resolveMu    sync.Mutex
	resolveCache map[resolveCacheKey]resolveCacheEntry
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

const negativeResolveTTL = time.Second

func NewReader(config Config) (*Reader, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &Reader{
		config:       normalized,
		resolveCache: make(map[resolveCacheKey]resolveCacheEntry),
	}, nil
}

func (r *Reader) Read(ctx context.Context, request Request) ([]Event, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var path string
	var err error
	switch request.Backend {
	case BackendClaude:
		path, err = r.resolveClaude(request)
	case BackendCodex:
		path, err = r.resolveCodex(ctx, request)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedBackend, request.Backend)
	}
	if err != nil {
		return nil, err
	}

	lines, err := readRecentJSONLLines(path, r.config.MaxReadBytes, r.config.MaxLineBytes)
	if err != nil {
		return nil, err
	}
	var events []Event
	switch request.Backend {
	case BackendClaude:
		events = parseClaude(lines, r.config.MaxBodyBytes)
	case BackendCodex:
		events = parseCodex(lines, r.config.MaxBodyBytes)
	}
	if len(events) > r.config.MaxEvents {
		events = events[len(events)-r.config.MaxEvents:]
	}
	return events, nil
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
