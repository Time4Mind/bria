package recoveryruntime

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

// NativeReader reads the adapter's private exact-session acceptance receipts.
// It never launches a provider or interprets native transcript prompt text.
type NativeReader struct {
	root     string
	provider domain.Provider
}

func NewNative(root string, provider domain.Provider) (*NativeReader, error) {
	if !filepath.IsAbs(root) || !utf8.ValidString(root) || strings.ContainsRune(root, 0) || provider != domain.ProviderCodex && provider != domain.ProviderClaude {
		return nil, ErrUnavailable
	}
	return &NativeReader{root: filepath.Clean(root), provider: provider}, nil
}

func (reader *NativeReader) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	unavailable := sessionruntime.AcceptedTurnReconciliation{}
	if reader == nil || ctx == nil || request.Provider != reader.provider || request.Binding.Provider != reader.provider || request.SessionID == "" || request.Binding.Generation == 0 || !nativeUUID(request.Binding.SessionID) || !filepath.IsAbs(request.Workdir) || strings.ContainsRune(request.Workdir, 0) {
		return unavailable, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return unavailable, err
	}
	root, err := os.Lstat(reader.root)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 || root.Mode().Perm()&0077 != 0 {
		return unavailable, ErrUnavailable
	}
	path := filepath.Join(reader.root, request.Binding.SessionID+".json")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > 1<<20 {
		return unavailable, ErrUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return unavailable, ErrUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return unavailable, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return unavailable, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return unavailable, err
	}
	turns, err := nativeReceipts(data, request.Binding.SessionID)
	if err != nil {
		return unavailable, ErrUnavailable
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return unavailable, ErrUnavailable
	}
	return sessionruntime.AcceptedTurnReconciliation{Turns: turns}, nil
}

func nativeUUID(id string) bool {
	if len(id) != 36 || id != strings.ToLower(id) || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(decoded) == 16
}

func nativeReceipts(data []byte, id string) ([]sessionruntime.ReconciledAcceptedTurn, error) {
	if !utf8.Valid(data) {
		return nil, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrUnavailable
	}
	seen := map[string]bool{}
	turns := []sessionruntime.ReconciledAcceptedTurn{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return nil, ErrUnavailable
		}
		seen[name] = true
		switch name {
		case "session_id":
			var actual string
			if decoder.Decode(&actual) != nil || actual != id {
				return nil, ErrUnavailable
			}
		case "receipts":
			token, err := decoder.Token()
			if err != nil || token != json.Delim('{') {
				return nil, ErrUnavailable
			}
			messages := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				message, ok := key.(string)
				if err != nil || !ok || message == "" || len(message) > 1024 || strings.ContainsAny(message, "\x00\r\n") || messages[message] || len(turns) >= 10000 {
					return nil, ErrUnavailable
				}
				messages[message] = true
				var outcome sessionruntime.AcceptedTurnOutcome
				if decoder.Decode(&outcome) != nil || outcome != sessionruntime.AcceptedTurnUnknown && outcome != sessionruntime.AcceptedTurnCompleted && outcome != sessionruntime.AcceptedTurnFailed {
					return nil, ErrUnavailable
				}
				turns = append(turns, sessionruntime.ReconciledAcceptedTurn{MessageID: message, Outcome: outcome})
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim('}') {
				return nil, ErrUnavailable
			}
		default:
			return nil, ErrUnavailable
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || !seen["session_id"] || !seen["receipts"] {
		return nil, ErrUnavailable
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, ErrUnavailable
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].MessageID < turns[j].MessageID })
	return turns, nil
}

var _ sessionruntime.AcceptedTurnReader = (*NativeReader)(nil)
