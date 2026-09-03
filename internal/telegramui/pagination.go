package telegramui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PageLimits are caller-selected positive bounds for a rendered Telegram page.
type PageLimits struct {
	MaxRunes int
	MaxBytes int
}

// ContentBlock is an already-rendered, stably anchored section of card content.
// BreakBefore starts a new page without inserting or removing content.
type ContentBlock struct {
	Anchor      string
	Content     string
	BreakBefore bool
}

// ContentPage is a bounded content fragment and the stable block anchors it
// contains. Anchors carry no Telegram callback or entity identity.
type ContentPage struct {
	Content string
	Anchors []string
}

type ContentPagination struct {
	Pages []ContentPage
}

// PaginateContent deterministically divides rendered blocks without changing
// their bytes. Concatenating every page's Content reconstructs the exact input.
func PaginateContent(blocks []ContentBlock, limits PageLimits) (ContentPagination, error) {
	if limits.MaxRunes < 1 || limits.MaxBytes < 1 {
		return ContentPagination{}, fmt.Errorf("page rune and byte limits must be positive")
	}
	seenAnchors := make(map[string]struct{}, len(blocks))
	pages := make([]ContentPage, 0, 1)
	current := ContentPage{}
	flush := func() {
		pages = append(pages, current)
		current = ContentPage{}
	}

	for _, block := range blocks {
		if block.Anchor == "" {
			return ContentPagination{}, fmt.Errorf("content block anchor must be non-empty")
		}
		if strings.ContainsRune(block.Anchor, '\x00') {
			return ContentPagination{}, fmt.Errorf("content block anchor contains reserved separator")
		}
		if _, exists := seenAnchors[block.Anchor]; exists {
			return ContentPagination{}, fmt.Errorf("content block anchor must be unique")
		}
		seenAnchors[block.Anchor] = struct{}{}
		if block.BreakBefore && current.Content != "" {
			flush()
		}
		chunks, err := splitBoundedContent(block.Content, limits)
		if err != nil {
			return ContentPagination{}, err
		}
		for index, chunk := range chunks {
			candidate := current.Content + chunk
			if current.Content != "" && contentExceeds(candidate, limits) {
				flush()
			}
			current.Content += chunk
			current.Anchors = appendAnchor(current.Anchors, chunkAnchor(block.Anchor, index))
			if index < len(chunks)-1 {
				flush()
			}
		}
	}
	if current.Content != "" || len(pages) == 0 {
		flush()
	}
	if err := validateContentPages(pages); err != nil {
		return ContentPagination{}, err
	}
	return ContentPagination{Pages: pages}, nil
}

func chunkAnchor(anchor string, chunk int) string {
	if anchor == "" || chunk == 0 {
		return anchor
	}
	return anchor + "\x00" + strconv.Itoa(chunk)
}

func splitBoundedContent(content string, limits PageLimits) ([]string, error) {
	if content == "" {
		return nil, nil
	}
	if !utf8.ValidString(content) {
		return nil, fmt.Errorf("card content must be valid UTF-8")
	}
	chunks := make([]string, 0, 1)
	for len(content) > 0 {
		end := 0
		runes := 0
		bytes := 0
		for index, value := range content {
			size := utf8.RuneLen(value)
			if size < 1 {
				size = 1
			}
			if runes+1 > limits.MaxRunes || bytes+size > limits.MaxBytes {
				break
			}
			runes++
			bytes += size
			end = index + size
		}
		if end == 0 {
			return nil, fmt.Errorf("page byte limit cannot contain the next encoded rune")
		}
		chunks = append(chunks, content[:end])
		content = content[end:]
	}
	return chunks, nil
}

func contentExceeds(content string, limits PageLimits) bool {
	return utf8.RuneCountInString(content) > limits.MaxRunes || len(content) > limits.MaxBytes
}

func appendAnchor(anchors []string, anchor string) []string {
	if anchor == "" {
		return anchors
	}
	if len(anchors) == 0 || anchors[len(anchors)-1] != anchor {
		return append(anchors, anchor)
	}
	return anchors
}
