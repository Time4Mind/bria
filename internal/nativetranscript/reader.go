// Package nativetranscript incrementally reads one exactly bound native CLI
// transcript. It never infers completion from a terminal screen or starts a CLI.
package nativetranscript

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"bria/internal/nativejsonline"
	"bria/internal/runtimeprotocol"
)

// RecordLimitError identifies a record/projection budget failure, unlike the
// ErrLimit returned for invalid options or Drain's bounded number of batches.
// Observed counts bytes inspected in this record, not its unknown eventual size.
// Counts exclude the newline. No path or transcript payload is included.
type RecordLimitError struct {
	Offset, Observed, Limit int64
}

func (e *RecordLimitError) Error() string {
	return fmt.Sprintf("native transcript record exceeds limit (offset=%d observed=%d limit=%d)", e.Offset, e.Observed, e.Limit)
}

func (e *RecordLimitError) Unwrap() error { return ErrLimit }

var (
	ErrNotFound  = errors.New("native transcript not found yet")
	ErrBinding   = errors.New("native transcript binding unverifiable")
	ErrLimit     = errors.New("native transcript read limit exceeded")
	ErrMalformed = errors.New("native transcript record malformed")
)

type Kind string

const (
	KindUser        Kind = "user"
	KindCommentary  Kind = "commentary"
	KindFinal       Kind = "final"
	KindTool        Kind = "tool"
	KindQuestion    Kind = "question"
	KindThinking    Kind = "thinking"
	KindComplete    Kind = "complete"
	KindInterrupted Kind = "interrupted"
	KindModel       Kind = "model"
)

// Event contains one public transcript projection. Structured tool metadata is
// bounded and optional so existing Kind/Text consumers remain compatible.
// ID is stable for this exact file's byte offset and content-block ordinal.
type Event struct {
	ID        string
	Kind      Kind
	Text      string
	Model     string
	SessionID string
	TurnID    string
	Metadata  *runtimeprotocol.EventMetadata
}

type Options struct {
	Provider     string // codex or claude
	SessionID    string // exact native UUID, not Bria's session UUID
	Workdir      string
	Root         string // Codex sessions or Claude projects directory
	MaxEntries   int    // total directory entries inspected during exact lookup
	MaxPollBytes int    // maximum bytes parsed per Poll; default 4 MiB
	MaxLineBytes int    // record/projection byte budget; default 1 MiB; oversized auxiliary string values may be discarded
}

// Reader retains one open file and a bounded cursor, not the full history.
// Poll is serialized. Call Close when the owning session watcher stops.
type Reader struct {
	mu             sync.Mutex
	opts           Options
	file           *os.File
	path           string
	identity       os.FileInfo
	offset         int64
	pending        *nativejsonline.Line
	pendingStart   int64
	state          parseState
	titleIndexInfo os.FileInfo
	indexedTitle   string
}

func Open(ctx context.Context, opts Options) (*Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Provider != "codex" && opts.Provider != "claude" || !uuid(opts.SessionID) || !filepath.IsAbs(opts.Root) || !filepath.IsAbs(opts.Workdir) {
		return nil, ErrBinding
	}
	if opts.MaxEntries == 0 {
		opts.MaxEntries = 20000
	}
	if opts.MaxPollBytes == 0 {
		opts.MaxPollBytes = 4 << 20
	}
	if opts.MaxLineBytes == 0 {
		opts.MaxLineBytes = 1 << 20
	}
	if opts.MaxEntries < 1 || opts.MaxEntries > 100000 || opts.MaxLineBytes < 128 || opts.MaxPollBytes <= opts.MaxLineBytes || opts.MaxPollBytes > 64<<20 {
		return nil, ErrLimit
	}
	opts.Root = filepath.Clean(opts.Root)
	opts.Workdir = filepath.Clean(opts.Workdir)
	// Resolve the explicitly configured root once. Descendant symlinks are never followed.
	resolved, err := filepath.EvalSymlinks(opts.Root)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, ErrBinding
	}
	opts.Root = resolved
	path, err := find(ctx, opts)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, ErrBinding
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrBinding
	}
	fail := func(err error) (*Reader, error) { file.Close(); return nil, err }
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return fail(ErrBinding)
	}
	// Exact filename alone is insufficient. Verify native identity and cwd before
	// exposing any conversation text. A not-yet-written header is retryable.
	data := make([]byte, opts.MaxPollBytes)
	n, err := file.ReadAt(data, 0)
	if err != nil && err != io.EOF {
		return fail(ErrBinding)
	}
	if err = verifyHeader(ctx, data[:n], opts); err != nil {
		if errors.Is(err, ErrLimit) {
			for offset := 0; offset < n; {
				length := bytes.IndexByte(data[offset:n], '\n')
				if length < 0 {
					length = n - offset
				}
				if length > opts.MaxLineBytes {
					err = &RecordLimitError{int64(offset), int64(length), int64(opts.MaxLineBytes)}
					break
				}
				offset += length + 1
			}
		}
		return fail(err)
	}
	return &Reader{opts: opts, file: file, path: path, identity: after}, nil
}

func uuid(s string) bool {
	if len(s) != 36 || s != strings.ToLower(s) || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil && len(b) == 16
}

func find(ctx context.Context, opts Options) (string, error) {
	found := ""
	remaining := opts.MaxEntries
	var visit func(string, int) error
	visit = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// ReadDir(n) bounds memory even for enormous unrelated native stores.
		before, err := os.Lstat(dir)
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return ErrBinding
		}
		f, err := os.Open(dir)
		if err != nil {
			return ErrBinding
		}
		defer f.Close()
		after, err := f.Stat()
		if err != nil || !os.SameFile(before, after) {
			return ErrBinding
		}
		for {
			entries, readErr := f.ReadDir(min(128, remaining+1))
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				remaining--
				if remaining < 0 {
					return ErrLimit
				}
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				path := filepath.Join(dir, entry.Name())
				if entry.IsDir() {
					if depth > 0 {
						if err := visit(path, depth-1); err != nil {
							return err
						}
					}
					continue
				}
				name := entry.Name()
				match := name == opts.SessionID+".jsonl"
				if opts.Provider == "codex" {
					match = strings.HasSuffix(name, "-"+opts.SessionID+".jsonl")
				}
				if match {
					if found != "" {
						return ErrBinding
					}
					found = path
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return ErrBinding
			}
		}
		return nil
	}
	depth := 1
	if opts.Provider == "codex" {
		depth = 3
	}
	if err := visit(opts.Root, depth); err != nil {
		return "", err
	}
	if found == "" {
		return "", ErrNotFound
	}
	return found, nil
}

func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// Poll emits each committed complete line once. Partial trailing writes remain
// in a bounded checkpoint for the next call. Errors commit neither events nor
// cursor/parse state for the failed batch. Malformed/replaced/truncated files fail closed;
// callers must not guess a different session in response to those errors.
func (r *Reader) Poll(ctx context.Context) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pollLocked(ctx, -1)
}

// Drain establishes a resumed-session baseline without replaying old events.
// It consumes through the file size captured at entry (not an ever-growing EOF),
// keeping model/turn parse state. At most 128 bounded batches are consumed. A
// partial trailing baseline record returns ErrNotFound and can be retried once
// the provider finishes writing it; errors must not be treated as a baseline.
func (r *Reader) Drain(ctx context.Context) error {
	return r.Scan(ctx, nil)
}

// Scan visits from the current cursor through the file size captured at entry,
// with the same 128-batch and partial-record limits as Drain. Only nil proves
// that entire prefix was consumed; discard tentative observations on any error.
// The file is provider-owned append-only data, not an immutable snapshot.
// The visitor runs under the Reader lock and must not call Reader methods.
// Reopen the Reader to retry a failed scan of the full history.
func (r *Reader) Scan(ctx context.Context, visit func([]Event) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.file == nil {
		return ErrBinding
	}
	info, err := r.file.Stat()
	if err != nil || !os.SameFile(r.identity, info) || info.Size() < r.offset {
		return ErrBinding
	}
	end := info.Size()
	for pass := 0; pass < 128; pass++ {
		before := r.offset
		events, err := r.pollLocked(ctx, end)
		if err != nil {
			return err
		}
		if visit != nil {
			if err := visit(events); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.offset == end && r.pending == nil {
			return nil
		}
		if r.offset == before {
			return ErrNotFound
		}
	}
	return ErrLimit
}

func (r *Reader) pollLocked(ctx context.Context, limit int64) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.file == nil {
		return nil, ErrBinding
	}
	current, err := os.Lstat(r.path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(r.identity, current) || current.Size() < r.offset {
		return nil, ErrBinding
	}
	end := current.Size()
	if limit >= 0 {
		if end < limit {
			return nil, ErrBinding
		}
		end = min(end, limit)
	}
	data := make([]byte, min(int64(r.opts.MaxPollBytes), end-r.offset))
	n, err := r.file.ReadAt(data, r.offset)
	if err != nil && err != io.EOF {
		return nil, ErrBinding
	}
	data = data[:n]
	offset, state := r.offset, r.state
	pending, pendingStart := r.pending.Clone(), r.pendingStart
	events := []Event{}
	for _, b := range data {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if pending == nil {
			pending = &nativejsonline.Line{Max: r.opts.MaxLineBytes}
			pendingStart = offset
		}
		done, frameErr := pending.Push(b)
		if frameErr != nil {
			if errors.Is(frameErr, nativejsonline.ErrLimit) || pending.Large {
				return nil, &RecordLimitError{pendingStart, pending.Size, int64(r.opts.MaxLineBytes)}
			}
			return nil, ErrMalformed
		}
		offset++
		if !done {
			continue
		}
		line := pending.Bytes()
		if pending.Large {
			rec, decodeErr := decode(line)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if r.opts.Provider != "codex" || rec.Type != "event_msg" || rec.Payload.Type != "item_completed" {
				return nil, &RecordLimitError{pendingStart, pending.Size, int64(r.opts.MaxLineBytes)}
			}
		}
		parsed, err := state.parse(line, r.opts, pendingStart)
		if err != nil {
			return nil, err
		}
		events = append(events, parsed...)
		pending = nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.offset, r.state = offset, state
	r.pending, r.pendingStart = pending, pendingStart
	return events, nil
}
