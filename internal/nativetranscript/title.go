package nativetranscript

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxTitleIndexBytes = 512 << 10

// Title reads provider-owned display metadata only. It never derives a title
// from user prompts. Claude metadata follows the existing transcript cursor;
// Codex reads at most a bounded tail of its one known append-only name index.
func (r *Reader) Title(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r.file == nil {
		return "", ErrBinding
	}
	if r.opts.Provider == "claude" {
		return r.state.title, nil
	}
	path := filepath.Join(filepath.Dir(r.opts.Root), "session_index.jsonl")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrBinding
	}
	if old := r.titleIndexInfo; old != nil && os.SameFile(old, info) && old.Size() == info.Size() && old.ModTime() == info.ModTime() {
		return r.indexedTitle, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrBinding
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", ErrBinding
	}
	start := max(int64(0), info.Size()-maxTitleIndexBytes)
	data := make([]byte, info.Size()-start)
	n, err := file.ReadAt(data, start)
	if err != nil && err != io.EOF {
		return "", ErrBinding
	}
	data = data[:n]
	if start > 0 {
		if boundary := bytes.IndexByte(data, '\n'); boundary >= 0 {
			data = data[boundary+1:]
		} else {
			return "", nil
		}
	}
	title := ""
	if old := r.titleIndexInfo; old != nil && os.SameFile(old, info) && info.Size() >= old.Size() {
		title = r.indexedTitle
	}
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		line := data[:end]
		data = data[end+1:]
		var entry struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if uniqueJSON(line) != nil || json.Unmarshal(line, &entry) != nil {
			return "", ErrMalformed
		}
		if entry.ID == r.opts.SessionID && validTitle(entry.Name) {
			title = entry.Name
		}
	}
	r.titleIndexInfo, r.indexedTitle = info, title
	return title, nil
}

func validTitle(title string) bool {
	if title == "" || len(title) > 1024 || !utf8.ValidString(title) || strings.TrimSpace(title) != title {
		return false
	}
	for _, char := range title {
		if char < 32 || char == 127 {
			return false
		}
	}
	return true
}
