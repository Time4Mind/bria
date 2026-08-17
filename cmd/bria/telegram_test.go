package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/telegrambot"
)

func TestTelegramTimingLogSuffixMeasuresHandlerAndMessageAge(t *testing.T) {
	startedAt := time.Unix(102, 250_000_000)
	finishedAt := time.Unix(102, 700_000_000)
	suffix := telegramTimingLogSuffix(telegrambot.IncomingUpdate{
		Kind: telegrambot.IncomingMessage, MessageDate: 100,
	}, startedAt, finishedAt)
	if !strings.Contains(suffix, "handle_ms=450") ||
		!strings.Contains(suffix, "telegram_age_ms=2700") ||
		!strings.Contains(suffix, "at=1970-01-01T00:01:42.7Z") {
		t.Fatalf("timing suffix = %q", suffix)
	}
}

func TestLoadOptionalTelegramToken(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing")
	if token, enabled, err := loadOptionalTelegramToken(missing); err != nil || enabled || token != "" {
		t.Fatalf("missing token=(%q, %v, %v)", token, enabled, err)
	}

	path := filepath.Join(directory, "telegram.token")
	if err := os.WriteFile(path, []byte("123456:abc_DEF-ghi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, enabled, err := loadOptionalTelegramToken(path)
	if err != nil || !enabled || token != "123456:abc_DEF-ghi" {
		t.Fatalf("token=(%q, %v, %v)", token, enabled, err)
	}

	if err := os.WriteFile(path, []byte(strings.Repeat("x", 513)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOptionalTelegramToken(path); err == nil {
		t.Fatal("oversized Telegram token file accepted")
	}
}

func TestTelegramCallbackLogSuffixOmitsOpaqueToken(t *testing.T) {
	update := telegrambot.IncomingUpdate{
		Kind:           telegrambot.IncomingCallback,
		CallbackData:   "page_latest:secret-opaque-token",
		CallbackOrigin: telegrambot.Message{MessageID: 73},
	}
	suffix := telegramCallbackLogSuffix(update)
	if suffix != " action=page_latest card=73" {
		t.Fatalf("suffix = %q", suffix)
	}
	if strings.Contains(suffix, "secret") {
		t.Fatal("callback token leaked into log suffix")
	}
}

func TestTelegramCallbackLogSuffixRejectsUnknownAction(t *testing.T) {
	update := telegrambot.IncomingUpdate{
		Kind:           telegrambot.IncomingCallback,
		CallbackData:   "unknown:secret",
		CallbackOrigin: telegrambot.Message{MessageID: 91},
	}
	if suffix := telegramCallbackLogSuffix(update); suffix != " action=invalid card=91" {
		t.Fatalf("suffix = %q", suffix)
	}
	if suffix := telegramCallbackLogSuffix(telegrambot.IncomingUpdate{
		Kind: telegrambot.IncomingMessage,
	}); suffix != "" {
		t.Fatalf("message suffix = %q", suffix)
	}
}

func TestLoadTelegramTokenRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "telegram.token")
	if err := os.WriteFile(path, []byte("123:token"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOptionalTelegramToken(path); err == nil {
		t.Fatal("world-readable Telegram token accepted")
	}
}
