package transcript

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
)

type codexRow struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Ordinal   json.RawMessage `json:"ordinal"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Message   string          `json:"message"`
	Phase     string          `json:"phase"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Content   json.RawMessage `json:"content"`
	Summary   json.RawMessage `json:"summary"`
	IsError   bool            `json:"is_error"`
	Info      *codexTokenInfo `json:"info"`
}

type codexTokenInfo struct {
	LastTokenUsage     *codexTokenUsage `json:"last_token_usage"`
	ModelContextWindow int              `json:"model_context_window"`
}

type codexTokenUsage struct {
	TotalTokens int `json:"total_tokens"`
}

func parseCodex(lines [][]byte, maxBodyBytes int) []Event {
	events := make([]Event, 0, len(lines))
	var contextPercent *int
	for _, line := range lines {
		var row codexRow
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		var payload codexPayload
		if json.Unmarshal(row.Payload, &payload) != nil {
			continue
		}
		switch row.Type {
		case "event_msg":
			if payload.Type == "token_count" {
				contextPercent = codexContextPercent(payload.Info)
				continue
			}
			if event, ok := parseCodexEventMessage(payload, row.Timestamp, maxBodyBytes); ok {
				events = append(events, event)
			}
		case "response_item":
			events = append(events, parseCodexResponseItem(row, payload, maxBodyBytes)...)
		}
	}
	if len(events) > 0 && contextPercent != nil {
		events[len(events)-1].ContextPercent = contextPercent
	}
	return events
}

func codexContextPercent(info *codexTokenInfo) *int {
	if info == nil || info.LastTokenUsage == nil || info.ModelContextWindow <= 0 ||
		info.LastTokenUsage.TotalTokens < 0 {
		return nil
	}
	percent := int(math.Round(float64(info.LastTokenUsage.TotalTokens) * 100 /
		float64(info.ModelContextWindow)))
	percent = min(100, max(0, percent))
	return &percent
}

func parseCodexEventMessage(payload codexPayload, timestamp string, maxBodyBytes int) (Event, bool) {
	text := strings.TrimSpace(payload.Message)
	if text == "" {
		return Event{}, false
	}
	switch payload.Type {
	case "user_message":
		if codexServiceUserText(text) {
			return Event{}, false
		}
		return Event{Kind: EventUserText, Text: bounded(text, maxBodyBytes), Timestamp: timestamp}, true
	case "agent_message":
		kind := EventAssistantText
		if payload.Phase == "final" || payload.Phase == "final_answer" {
			kind = EventAssistantFinal
		}
		return Event{Kind: kind, Text: bounded(text, maxBodyBytes), Timestamp: timestamp}, true
	case "agent_reasoning":
		return Event{
			Kind: EventThinking, Text: bounded(text, maxBodyBytes),
			Body: bounded(text, maxBodyBytes), Timestamp: timestamp,
		}, true
	default:
		return Event{}, false
	}
}

func parseCodexResponseItem(row codexRow, payload codexPayload, maxBodyBytes int) []Event {
	switch payload.Type {
	case "message":
		// Older Codex versions emitted these alongside event_msg. Newer
		// versions add ordinal when response_item is the authoritative row.
		if len(row.Ordinal) == 0 || bytes.Equal(row.Ordinal, []byte("null")) {
			return nil
		}
		text := extractTextContent(payload.Content, maxBodyBytes)
		if text == "" {
			return nil
		}
		kind := EventUserText
		if payload.Role == "assistant" {
			kind = EventAssistantText
			if payload.Phase == "final" || payload.Phase == "final_answer" {
				kind = EventAssistantFinal
			}
		} else if payload.Role != "user" {
			return nil
		} else {
			text = extractCodexUserContent(payload.Content, maxBodyBytes)
			if text == "" {
				return nil
			}
		}
		return []Event{{Kind: kind, Text: text, Timestamp: row.Timestamp}}
	case "reasoning":
		text := extractTextContent(payload.Summary, maxBodyBytes)
		if text == "" {
			return nil
		}
		return []Event{{Kind: EventThinking, Text: text, Body: text, Timestamp: row.Timestamp}}
	case "function_call", "custom_tool_call":
		id := payload.CallID
		if id == "" {
			id = payload.ID
		}
		name := strings.TrimSpace(payload.Name)
		if name == "" {
			name = "tool"
		}
		arguments := payload.Arguments
		if len(arguments) == 0 {
			arguments = payload.Input
		}
		head, body := codexToolCall(name, arguments, maxBodyBytes)
		return []Event{{
			Kind: EventToolCall, ToolUseID: id, ToolName: head, Head: head,
			Body: body, Timestamp: row.Timestamp,
		}}
	case "function_call_output", "custom_tool_call_output":
		id := payload.CallID
		if id == "" {
			id = payload.ID
		}
		return []Event{{
			Kind: EventToolResult, ToolUseID: id, Head: "Tool result",
			Body:  extractTextContent(payload.Output, maxBodyBytes),
			Error: payload.IsError, Timestamp: row.Timestamp,
		}}
	default:
		return nil
	}
}

// Codex records host-provided turn context as role=user response items even
// though it was not typed by the user. Filter those structured envelopes at
// the transcript boundary so Telegram never attributes them to the person.
func extractCodexUserContent(raw json.RawMessage, maxBodyBytes int) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		text := extractTextContent(raw, maxBodyBytes)
		if codexServiceUserText(text) {
			return ""
		}
		return text
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		text := extractTextContent(block, maxBodyBytes)
		if text == "" || codexServiceUserText(text) {
			continue
		}
		parts = append(parts, text)
	}
	return bounded(strings.Join(parts, "\n"), maxBodyBytes)
}

func codexServiceUserText(text string) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<environment_context>") &&
		strings.HasSuffix(text, "</environment_context>") {
		return true
	}
	return strings.HasPrefix(text, "# AGENTS.md instructions for ") &&
		strings.Contains(text, "\n<INSTRUCTIONS>") &&
		strings.HasSuffix(text, "</INSTRUCTIONS>")
}
