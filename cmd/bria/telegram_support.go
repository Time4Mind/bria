package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/interaction"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func clusterAgentWorkdir(nodeConfig config.Config) string {
	if current, err := os.Getwd(); err == nil {
		if _, statErr := os.Stat(filepath.Join(current, "go.mod")); statErr == nil {
			return current
		}
	}
	return nodeConfig.EffectiveUpdateInstallRoot()
}

func telegramInteractionIngress(update telegrambot.IncomingUpdate) (interaction.Ingress, error) {
	return interaction.NewIngress(
		"telegram", fmt.Sprintf("update:%d", update.UpdateID), string(update.Kind),
	)
}

func telegramFailureClass(err error) processlog.FailureClass {
	if err == nil {
		return processlog.FailureNone
	}
	if _, limited := telegrambot.FloodWait(err); limited {
		return processlog.FailureRateLimited
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return processlog.FailureTimeout
	case errors.Is(err, context.Canceled):
		return processlog.FailureCancelled
	case errors.Is(err, domain.ErrAccessDenied):
		return processlog.FailurePermission
	case errors.Is(err, domain.ErrNotFound):
		return processlog.FailureNotFound
	case errors.Is(err, domain.ErrInvalidState):
		return processlog.FailureInvalidState
	case errors.Is(err, domain.ErrStaleOperation):
		return processlog.FailureStaleState
	}
	var apiErr *telegrambot.APIError
	if errors.As(err, &apiErr) {
		return processlog.FailureTransport
	}
	var transportErr *telegrambot.TransportError
	if errors.As(err, &transportErr) {
		return processlog.FailureTransport
	}
	return processlog.FailureInternal
}

func telegramTimingLogSuffix(
	update telegrambot.IncomingUpdate,
	startedAt time.Time,
	finishedAt time.Time,
) string {
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	suffix := fmt.Sprintf(" finished_at=%s handle_ms=%d",
		finishedAt.UTC().Format(time.RFC3339Nano), finishedAt.Sub(startedAt).Milliseconds())
	if update.Kind == telegrambot.IncomingMessage && update.MessageDate > 0 {
		messageAt := time.Unix(update.MessageDate, 0)
		if !finishedAt.Before(messageAt) {
			suffix += fmt.Sprintf(" telegram_age_ms=%d", finishedAt.Sub(messageAt).Milliseconds())
		}
	}
	return suffix
}

// telegramCallbackLogSuffix identifies a failed UI route without persisting
// its signed opaque token. Tokens can reference cluster objects and must not
// become part of operational logs.
func telegramCallbackLogSuffix(update telegrambot.IncomingUpdate) string {
	if update.Kind != telegrambot.IncomingCallback {
		return ""
	}
	callback, err := telegramui.DecodeCallback(update.CallbackData)
	if err != nil {
		return fmt.Sprintf(" action=invalid card=%d", update.CallbackOrigin.MessageID)
	}
	return fmt.Sprintf(" action=%s card=%d", callback.Action, update.CallbackOrigin.MessageID)
}

func loadOptionalTelegramToken(path string) (string, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open Telegram token file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("inspect Telegram token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 512 {
		return "", false, errors.New("Telegram token file must be a small regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", false, errors.New("Telegram token file permissions must not allow group or other access")
	}
	data, err := io.ReadAll(io.LimitReader(file, 513))
	if err != nil {
		return "", false, fmt.Errorf("read Telegram token file: %w", err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false, errors.New("Telegram token file contains binary data")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", false, nil
	}
	return token, true, nil
}
