package nativetranscript

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"bria/internal/runtimeprotocol"
)

type record struct {
	CustomTitle string  `json:"customTitle"`
	AITitle     string  `json:"aiTitle"`
	Type        string  `json:"type"`
	SessionID   string  `json:"sessionId"`
	Cwd         string  `json:"cwd"`
	UUID        string  `json:"uuid"`
	IsMeta      bool    `json:"isMeta"`
	IsSidechain bool    `json:"isSidechain"`
	Payload     payload `json:"payload"`
	Message     message `json:"message"`
}
type payload struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	TurnID    string          `json:"turn_id"`
	Model     string          `json:"model"`
	Message   string          `json:"message"`
	Phase     string          `json:"phase"`
	Role      string          `json:"role"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Status    string          `json:"status"`
	Content   json.RawMessage `json:"content"`
	Metadata  struct {
		TurnID string   `json:"turn_id"`
		Kinds  []string `json:"content_item_kinds"`
	} `json:"internal_chat_message_metadata_passthrough"`
}
type message struct {
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
}
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}
type parseState struct {
	model, turn, lastFinal, finalSource, title string
	customTitle                                bool
}

func decode(line []byte) (record, error) {
	var rec record
	if !utf8.Valid(line) || uniqueJSON(line) != nil || json.Unmarshal(line, &rec) != nil || rec.Type == "" {
		return rec, ErrMalformed
	}
	return rec, nil
}

// Duplicate identity keys must never make an ambiguous record appear bound.
func uniqueJSON(line []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return ErrMalformed
		}
		token, err := decoder.Token()
		if err != nil {
			return ErrMalformed
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return ErrMalformed
		}
		seen := map[string]bool{}
		for decoder.More() {
			if delim == '{' {
				key, err := decoder.Token()
				if err != nil {
					return ErrMalformed
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrMalformed
				}
				seen[name] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || delim == '{' && closing != json.Delim('}') || delim == '[' && closing != json.Delim(']') {
			return ErrMalformed
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrMalformed
	}
	return nil
}

func verifyHeader(ctx context.Context, data []byte, opts Options) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			if len(data) > opts.MaxLineBytes {
				return ErrLimit
			}
			return ErrNotFound
		}
		if end > opts.MaxLineBytes {
			return ErrLimit
		}
		rec, err := decode(data[:end])
		if err != nil {
			return err
		}
		if opts.Provider == "codex" {
			if rec.Type != "session_meta" || rec.Payload.ID != opts.SessionID || filepath.Clean(rec.Payload.Cwd) != opts.Workdir {
				return ErrBinding
			}
			return nil
		}
		if rec.SessionID != "" && rec.SessionID != opts.SessionID {
			return ErrBinding
		}
		if rec.Cwd != "" && filepath.Clean(rec.Cwd) != opts.Workdir {
			return ErrBinding
		}
		if rec.SessionID == opts.SessionID && rec.Cwd != "" {
			return nil
		}
		// Claude may prepend structural file-history/queue records without cwd.
		if rec.Type == "user" || rec.Type == "assistant" {
			return ErrBinding
		}
		data = data[end+1:]
	}
	return ErrNotFound
}

func (s *parseState) parse(line []byte, opts Options, offset int64) ([]Event, error) {
	rec, err := decode(line)
	if err != nil {
		return nil, err
	}
	events := []Event{}
	emitMetadata := func(kind Kind, text string, metadata *runtimeprotocol.EventMetadata) {
		events = append(events, Event{ID: strconv.FormatInt(offset, 10) + ":" + strconv.Itoa(len(events)), Kind: kind, Text: text, Model: s.model, TurnID: s.turn, SessionID: opts.SessionID, Metadata: metadata})
	}
	emit := func(kind Kind, text string) { emitMetadata(kind, text, nil) }
	model := func(value string) {
		if value != "" && value != s.model {
			s.model = value
			emit(KindModel, "")
		}
	}
	final := func(text, source string) {
		if text == s.lastFinal && s.finalSource != "" && source != s.finalSource {
			return
		}
		s.lastFinal = text
		s.finalSource = source
		emit(KindFinal, text)
	}
	if opts.Provider == "codex" {
		p := rec.Payload
		switch rec.Type {
		case "session_meta":
			if p.ID != opts.SessionID || filepath.Clean(p.Cwd) != opts.Workdir {
				return nil, ErrBinding
			}
		case "turn_context":
			if p.TurnID != "" {
				s.turn = p.TurnID
			}
			model(p.Model)
		case "event_msg":
			if p.TurnID != "" {
				s.turn = p.TurnID
			}
			switch p.Type {
			case "task_started":
				s.lastFinal = ""
				s.finalSource = ""
			case "user_message":
				s.lastFinal = ""
				s.finalSource = ""
				emit(KindUser, p.Message)
			case "agent_message":
				if p.Phase == "final" || p.Phase == "final_answer" {
					final(p.Message, "event_msg")
				} else {
					emit(KindCommentary, p.Message)
				}
			case "agent_reasoning":
				thinking := p.Message
				if thinking == "" {
					thinking = p.Text
				}
				if thinking != "" {
					emit(KindThinking, boundedMetadataText(thinking))
				}
			case "task_complete":
				emit(KindComplete, "")
			case "turn_aborted":
				emit(KindInterrupted, "")
			}
		case "response_item":
			if p.Metadata.TurnID != "" {
				s.turn = p.Metadata.TurnID
			}
			if p.Type == "message" && p.Role == "user" {
				// Modern paginated histories annotate each user content block. Do
				// not echo injected instructions/environment or guess untyped rows.
				var blocks []block
				if json.Unmarshal(p.Content, &blocks) == nil && len(blocks) == len(p.Metadata.Kinds) {
					parts := []string{}
					for i, b := range blocks {
						if p.Metadata.Kinds[i] == "user.text" && b.Type == "input_text" {
							parts = append(parts, b.Text)
						}
					}
					if len(parts) > 0 {
						s.lastFinal = ""
						s.finalSource = ""
						emit(KindUser, strings.Join(parts, "\n"))
					}
				}
			}
			if p.Type == "reasoning" {
				if thinking := reasoningContent(p.Content); thinking != "" {
					emit(KindThinking, thinking)
				}
			}
			if p.Type == "function_call" || p.Type == "custom_tool_call" {
				name := p.Name
				if name == "" {
					name = "tool"
				}
				emitMetadata(KindTool, name, &runtimeprotocol.EventMetadata{
					ItemID: toolItemID(p.CallID, p.ID), Name: p.Name,
					Arguments: boundedMetadataText(firstRawText(p.Arguments, p.Input)), Status: toolStatus(p.Status, "in_progress"),
				})
			}
			if p.Type == "function_call_output" || p.Type == "custom_tool_call_output" {
				emitMetadata(KindTool, "tool", &runtimeprotocol.EventMetadata{
					ItemID: toolItemID(p.CallID, p.ID), Result: boundedMetadataText(firstRawText(p.Output, p.Content)), Status: toolStatus(p.Status, "completed"),
				})
			}
			// Native versions can record assistant phase on response_item instead of
			// event_msg. Ignore unphased mirrors and suppress cross-format final mirrors.
			if p.Type == "message" && p.Role == "assistant" && (p.Phase == "final" || p.Phase == "final_answer") {
				final(textContent(p.Content), "response_item")
			}
			if p.Type == "message" && p.Role == "assistant" && p.Phase == "commentary" {
				emit(KindCommentary, textContent(p.Content))
			}
		}
	} else {
		if rec.SessionID != "" && rec.SessionID != opts.SessionID || rec.Cwd != "" && filepath.Clean(rec.Cwd) != opts.Workdir {
			return nil, ErrBinding
		}
		if rec.IsSidechain || rec.IsMeta {
			return nil, nil
		}
		if rec.Type == "custom-title" || rec.Type == "ai-title" {
			if rec.SessionID != opts.SessionID {
				return nil, ErrBinding
			}
			title := rec.AITitle
			if rec.Type == "custom-title" {
				title = rec.CustomTitle
			}
			if validTitle(title) && (rec.Type == "custom-title" || !s.customTitle) {
				s.title = title
				s.customTitle = rec.Type == "custom-title"
			}
			return nil, nil
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			return nil, nil
		}
		if rec.SessionID != opts.SessionID {
			return nil, ErrBinding
		}
		text := textContent(rec.Message.Content)
		if rec.Type == "user" {
			if strings.HasPrefix(text, "<local-command-") || strings.HasPrefix(text, "<command-name>") {
				return nil, nil
			}
			if text != "" {
				s.turn = rec.UUID
				emit(KindUser, text)
			}
			var blocks []block
			if json.Unmarshal(rec.Message.Content, &blocks) == nil {
				for _, b := range blocks {
					if b.Type != "tool_result" {
						continue
					}
					status := "completed"
					if b.IsError {
						status = "failed"
					}
					emitMetadata(KindTool, "tool", &runtimeprotocol.EventMetadata{
						ItemID: b.ToolUseID, Result: boundedMetadataText(rawText(b.Content)), Status: status,
					})
				}
			}
		} else {
			model(rec.Message.Model)
			if text != "" {
				if rec.Message.StopReason == "end_turn" {
					emit(KindFinal, text)
				} else {
					emit(KindCommentary, text)
				}
			}
			var blocks []block
			if json.Unmarshal(rec.Message.Content, &blocks) == nil {
				for _, b := range blocks {
					if b.Type == "thinking" && b.Thinking != "" {
						emit(KindThinking, boundedMetadataText(b.Thinking))
					}
					if b.Type == "tool_use" {
						name := b.Name
						if name == "" {
							name = "tool"
						}
						emitMetadata(KindTool, name, &runtimeprotocol.EventMetadata{
							ItemID: b.ID, Name: b.Name, Arguments: boundedMetadataText(rawText(b.Input)), Status: "in_progress",
						})
						if question := userQuestion(b); question != "" {
							emit(KindQuestion, question)
						}
					}
				}
			}
			if rec.Message.StopReason == "end_turn" {
				emit(KindComplete, "")
			}
		}
	}
	return events, nil
}

func textContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := []string{}
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "output_text" || b.Type == "input_text" || b.Type == "summary_text" || b.Type == "reasoning_text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func reasoningContent(raw json.RawMessage) string {
	var parts []string
	if json.Unmarshal(raw, &parts) == nil {
		return boundedMetadataText(strings.Join(parts, "\n"))
	}
	return boundedMetadataText(textContent(raw))
}

func firstRawText(values ...json.RawMessage) string {
	for _, value := range values {
		if text := rawText(value); text != "" && text != "null" {
			return text
		}
	}
	return ""
}

func rawText(raw json.RawMessage) string {
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

func toolItemID(callID, itemID string) string {
	if callID != "" {
		return callID
	}
	return itemID
}

func toolStatus(status, fallback string) string {
	if status != "" {
		return boundedMetadataText(status)
	}
	return fallback
}

const metadataTextLimit = 8 << 10

func boundedMetadataText(value string) string {
	if len(value) <= metadataTextLimit {
		return value
	}
	value = value[:metadataTextLimit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
