// Package systemdeps requests narrowly scoped host package installation from
// the root-owned Bria system helper.
package systemdeps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Profile string

const (
	ProfileNodeJS Profile = "nodejs"
	ProfileSpeech Profile = "speech"
)

type Config struct {
	RequestDir string
	ResultDir  string
}

func Ensure(ctx context.Context, config Config, profile Profile, ready func() bool) error {
	if ready == nil || (profile != ProfileNodeJS && profile != ProfileSpeech) {
		return errors.New("invalid system dependency request")
	}
	if ready() {
		return nil
	}
	if !filepath.IsAbs(config.RequestDir) || !filepath.IsAbs(config.ResultDir) {
		return errors.New("automatic system dependency installer is unavailable")
	}
	if err := os.MkdirAll(config.RequestDir, 0o700); err != nil {
		return fmt.Errorf("prepare system dependency requests: %w", err)
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("create system dependency request identity: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	base := string(profile) + "-" + nonce
	requestPath := filepath.Join(config.RequestDir, base+".request")
	resultPath := filepath.Join(config.ResultDir, base+".result")
	temporary := requestPath + ".tmp"
	if err := os.WriteFile(temporary, []byte("install\n"), 0o600); err != nil {
		return fmt.Errorf("write system dependency request: %w", err)
	}
	if err := os.Rename(temporary, requestPath); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("activate system dependency request: %w", err)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return nil
		}
		if result, err := os.ReadFile(resultPath); err == nil {
			outcome := strings.TrimSpace(string(result))
			if outcome == "ready" {
				continue
			}
			if outcome == "" {
				outcome = "system dependency helper failed without details"
			}
			return errors.New(compact(outcome, 400))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func compact(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}
