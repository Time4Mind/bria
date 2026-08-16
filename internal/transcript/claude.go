package transcript

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
)

type claudeRow struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Error     string          `json:"error"`
	Message   json.RawMessage `json:"message"`
}

type claudeMessage struct {
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason"`
	Model      string          `json:"model"`
	Usage      claudeUsage     `json:"usage"`
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func parseClaude(lines [][]byte, maxBodyBytes int) []Event {
	events := make([]Event, 0, len(lines))
	var contextPercent *int
	for _, line := range lines {
		var row claudeRow
		if json.Unmarshal(line, &row) != nil || (row.Type != "user" && row.Type != "assistant") {
			continue
		}
		var message claudeMessage
		if json.Unmarshal(row.Message, &message) != nil {
			continue
		}
		blocks := decodeClaudeBlocks(message.Content)
		if row.Type == "user" {
			events = append(events, claudeUserEvents(blocks, row.Timestamp, maxBodyBytes)...)
			continue
		}
		events = append(events, claudeAssistantEvents(
			blocks, row.Timestamp, message.StopReason, row.Error, maxBodyBytes,
		)...)
		if percent := claudeContextPercent(message); percent != nil {
			contextPercent = percent
		}
	}
	if len(events) > 0 && contextPercent != nil {
		events[len(events)-1].ContextPercent = contextPercent
	}
	return events
}

func claudeContextPercent(message claudeMessage) *int {
	used := float64(message.Usage.InputTokens) + float64(message.Usage.CacheCreationInputTokens) +
		float64(message.Usage.CacheReadInputTokens)
	if used <= 0 {
		return nil
	}
	window := 1_000_000
	if strings.Contains(strings.ToLower(message.Model), "haiku") {
		window = 200_000
	}
	percent := int(math.Round(used * 100 / float64(window)))
	percent = min(100, max(0, percent))
	return &percent
}

func decodeClaudeBlocks(content json.RawMessage) []claudeBlock {
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		return nil
	}
	var rawBlocks []json.RawMessage
	if json.Unmarshal(content, &rawBlocks) == nil {
		blocks := make([]claudeBlock, 0, len(rawBlocks))
		for _, raw := range rawBlocks {
			var block claudeBlock
			if json.Unmarshal(raw, &block) == nil {
				blocks = append(blocks, block)
				continue
			}
			var text string
			if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
				blocks = append(blocks, claudeBlock{Type: "text", Text: text})
			}
		}
		return blocks
	}
	var text string
	if json.Unmarshal(content, &text) == nil && strings.TrimSpace(text) != "" {
		return []claudeBlock{{Type: "text", Text: text}}
	}
	return nil
}

func claudeAssistantEvents(
	blocks []claudeBlock,
	timestamp string,
	stopReason string,
	errorCode string,
	maxBodyBytes int,
) []Event {
	events := make([]Event, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text == "" || text == "(no content)" {
				continue
			}
			kind := EventAssistantText
			if stopReason == "end_turn" || stopReason == "stop" || errorCode != "" {
				kind = EventAssistantFinal
			}
			events = append(events, Event{
				Kind: kind, Text: bounded(text, maxBodyBytes), Timestamp: timestamp,
				Error: errorCode != "",
			})
		case "thinking":
			thinking := strings.TrimSpace(block.Thinking)
			if thinking != "" {
				events = append(events, Event{
					Kind: EventThinking, Text: bounded(thinking, maxBodyBytes),
					Body: bounded(thinking, maxBodyBytes), Timestamp: timestamp,
				})
			}
		case "tool_use":
			name := strings.TrimSpace(block.Name)
			if name == "" {
				name = "tool"
			}
			body := compactJSON(block.Input, maxBodyBytes)
			events = append(events, Event{
				Kind: EventToolCall, ToolUseID: block.ID, ToolName: name,
				Head: name, Body: body, Timestamp: timestamp,
			})
		}
	}
	return events
}

func claudeUserEvents(blocks []claudeBlock, timestamp string, maxBodyBytes int) []Event {
	events := make([]Event, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text != "" {
				events = append(events, Event{Kind: EventUserText, Text: bounded(text, maxBodyBytes), Timestamp: timestamp})
			}
		case "tool_result":
			body := extractTextContent(block.Content, maxBodyBytes)
			body = strings.ReplaceAll(body, "<tool_use_error>", "")
			body = strings.ReplaceAll(body, "</tool_use_error>", "")
			events = append(events, Event{
				Kind: EventToolResult, ToolUseID: block.ToolUseID,
				Head: "Tool result", Body: body, Error: block.IsError, Timestamp: timestamp,
			})
		}
	}
	return events
}

func compactJSON(raw json.RawMessage, maxBodyBytes int) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return ""
	}
	return bounded(compact.String(), maxBodyBytes)
}
