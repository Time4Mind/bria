package telegramapp

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/transcript"
)

const maxFilesPerAnswer = 4

var (
	markdownFilePattern = regexp.MustCompile(`\[[^\]\n]*\]\((?:file://)?<?([^)>\n]+)>?\)`)
	codeFilePattern     = regexp.MustCompile("`([^`\\n]+)`")
	absoluteFilePattern = regexp.MustCompile(`(?:^|[\s(])(/[^\s<>|]+)`)
	lineSuffixPattern   = regexp.MustCompile(`:[0-9]+(?::[0-9]+)?$`)
)

func (h *Handler) deliverFinalFiles(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	events []transcript.Event,
) {
	if h.controls.files == nil {
		return
	}
	text, timestamp := latestFinalText(events)
	if text == "" {
		return
	}
	for _, path := range localFileCandidates(text) {
		key := ref.Key() + "\x00" + timestamp + "\x00" + path
		if !h.claimDeliveredFile(key) {
			continue
		}

		file, err := h.controls.files.OpenSessionFile(ctx, actor, ref, path)
		if err != nil {
			h.forgetDeliveredFile(key)
			continue
		}
		_, err = h.messenger.SendDocument(ctx, telegrambot.DocumentRequest{
			ChatID: int64(actor.UserID), Name: file.Name, MIMEType: file.MIMEType,
			Size: file.Size, Content: file.Content,
		})
		_ = file.Content.Close()
		if err != nil {
			h.forgetDeliveredFile(key)
		}
	}
}

func (h *Handler) claimDeliveredFile(key string) bool {
	h.fileMu.Lock()
	defer h.fileMu.Unlock()
	if h.deliveredFiles[key] {
		return false
	}
	// This is an in-process duplicate guard, not durable state. Bound it so a
	// long-lived leader cannot accumulate one key per historical attachment.
	if len(h.deliveredFiles) >= 1024 {
		clear(h.deliveredFiles)
	}
	h.deliveredFiles[key] = true
	return true
}

func (h *Handler) forgetDeliveredFile(key string) {
	h.fileMu.Lock()
	delete(h.deliveredFiles, key)
	h.fileMu.Unlock()
}

func latestFinalText(events []transcript.Event) (string, string) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == transcript.EventAssistantFinal {
			return events[index].Text, events[index].Timestamp
		}
	}
	return "", ""
}

func localFileCandidates(text string) []string {
	result := make([]string, 0, maxFilesPerAnswer)
	seen := make(map[string]bool)
	appendCandidate := func(value string) {
		value = strings.TrimSpace(strings.Trim(value, `<>"'.,;!?()[]`))
		value = lineSuffixPattern.ReplaceAllString(value, "")
		if value == "" || strings.ContainsAny(value, "\r\n") ||
			(!filepath.IsAbs(value) && !strings.Contains(value, "/") && filepath.Ext(value) == "") ||
			seen[value] || len(result) >= maxFilesPerAnswer {
			return
		}
		seen[value] = true
		result = append(result, value)
	}
	for _, match := range markdownFilePattern.FindAllStringSubmatch(text, -1) {
		appendCandidate(match[1])
	}
	for _, match := range codeFilePattern.FindAllStringSubmatch(text, -1) {
		appendCandidate(strings.TrimPrefix(match[1], "file://"))
	}
	for _, match := range absoluteFilePattern.FindAllStringSubmatch(text, -1) {
		appendCandidate(match[1])
	}
	return result
}
