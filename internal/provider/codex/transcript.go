package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"bria/internal/runtimeprotocol"
)

type transcriptItem struct {
	ThreadID string
	TurnID   string
	Kind     string
	Text     string
	Metadata *runtimeprotocol.EventMetadata
}

func decodeTranscriptItem(method string, params json.RawMessage, maxTextBytes int) (transcriptItem, bool) {
	if method != "item/started" && method != "item/completed" {
		return transcriptItem{}, false
	}
	var envelope struct {
		ThreadID string          `json:"threadId"`
		TurnID   string          `json:"turnId"`
		Item     json.RawMessage `json:"item"`
	}
	if json.Unmarshal(params, &envelope) != nil || envelope.ThreadID == "" || envelope.TurnID == "" || len(envelope.Item) == 0 {
		return transcriptItem{}, false
	}
	var item struct {
		Type             string          `json:"type"`
		ID               string          `json:"id"`
		Text             string          `json:"text"`
		Phase            string          `json:"phase"`
		Summary          []string        `json:"summary"`
		Content          []string        `json:"content"`
		Command          string          `json:"command"`
		CWD              string          `json:"cwd"`
		Status           string          `json:"status"`
		AggregatedOutput *string         `json:"aggregatedOutput"`
		Changes          json.RawMessage `json:"changes"`
		Arguments        json.RawMessage `json:"arguments"`
		Result           json.RawMessage `json:"result"`
		Output           json.RawMessage `json:"output"`
		Error            json.RawMessage `json:"error"`
		ContentItems     json.RawMessage `json:"contentItems"`
		Results          json.RawMessage `json:"results"`
		AgentsStates     json.RawMessage `json:"agentsStates"`
		Name             string          `json:"name"`
		Namespace        string          `json:"namespace"`
		Server           string          `json:"server"`
		Tool             string          `json:"tool"`
		Query            string          `json:"query"`
		Path             string          `json:"path"`
		Prompt           *string         `json:"prompt"`
	}
	if json.Unmarshal(envelope.Item, &item) != nil {
		return transcriptItem{}, false
	}
	base := transcriptItem{ThreadID: envelope.ThreadID, TurnID: envelope.TurnID}
	if item.Type == "agentMessage" && item.Phase == "commentary" && validAdapterText(item.Text, maxTextBytes) {
		base.Kind, base.Text = "commentary", item.Text
		return base, true
	}
	if item.Type == "reasoning" && method == "item/completed" {
		text := strings.Join(item.Summary, "\n")
		if text == "" {
			text = strings.Join(item.Content, "\n")
		}
		if text == "" || !validAdapterText(text, maxTextBytes) {
			return transcriptItem{}, false
		}
		base.Kind, base.Text = "thinking", text
		return base, true
	}

	name, arguments, result := "", "", ""
	switch item.Type {
	case "commandExecution":
		name, arguments = "command", item.Command
		if item.CWD != "" {
			arguments = strings.TrimSpace(arguments + "\n" + item.CWD)
		}
		if item.AggregatedOutput != nil {
			result = *item.AggregatedOutput
		}
	case "fileChange":
		name, arguments = "file_change", displayJSON(item.Changes)
	case "mcpToolCall":
		name, arguments = item.Tool, displayJSON(item.Arguments)
		if item.Server != "" {
			name = item.Server + "." + name
		}
		result = firstNonEmptyJSON(item.Result, item.Error)
	case "dynamicToolCall":
		name, arguments, result = item.Tool, displayJSON(item.Arguments), displayJSON(item.ContentItems)
	case "webSearch":
		name, arguments, result = "web_search", item.Query, displayJSON(item.Results)
	case "imageView":
		name, arguments = "view_image", item.Path
	case "collabAgentToolCall":
		name, result = item.Tool, displayJSON(item.AgentsStates)
		if item.Prompt != nil {
			arguments = *item.Prompt
		}
	case "functionCallOutput":
		name, result = item.Name, displayJSON(item.Output)
	default:
		return transcriptItem{}, false
	}
	if name == "" {
		name = item.Type
	}
	status := item.Status
	if status == "" {
		if method == "item/started" {
			status = "in_progress"
		} else {
			status = "completed"
		}
	}
	budget := maxTextBytes / 4
	if budget < 1 {
		budget = 1
	}
	metadata := &runtimeprotocol.EventMetadata{
		ItemID: truncateUTF8(item.ID, budget), Name: truncateUTF8(name, budget),
		Arguments: truncateUTF8(arguments, budget), Result: truncateUTF8(result, budget), Status: truncateUTF8(status, budget),
	}
	base.Kind, base.Text, base.Metadata = "tool", metadata.Name, metadata
	return base, true
}

func firstNonEmptyJSON(values ...json.RawMessage) string {
	for _, value := range values {
		if result := displayJSON(value); result != "" && result != "null" {
			return result
		}
	}
	return ""
}

func displayJSON(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return ""
	}
	return compact.String()
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
