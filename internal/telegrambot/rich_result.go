package telegrambot

import (
	"encoding/json"
	"errors"
)

func parseRichResult(raw json.RawMessage, expected Message) (Message, error) {
	if string(raw) == "true" {
		if expected.ChatID <= 0 || expected.MessageID <= 0 {
			return Message{}, errors.New("Telegram rich result omitted message identity")
		}
		expected.Rich = true
		return expected, nil
	}
	var identity apiMessageResult
	if err := json.Unmarshal(raw, &identity); err != nil {
		return Message{}, errors.New("Telegram returned a malformed rich result")
	}
	message, err := validateMessageResult(identity, expected.ChatID)
	if err != nil {
		return Message{}, err
	}
	message.Rich = true
	message.RichMediaFileID = extractRichPhotoFileID(raw)
	return message, nil
}

func extractRichPhotoFileID(raw json.RawMessage) string {
	var payload any
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	if root, ok := payload.(map[string]any); ok {
		if rich, exists := root["rich_message"]; exists {
			payload = rich
		}
	}
	bestID, bestArea, bestSize := "", int64(-1), int64(-1)
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case []any:
			for _, nested := range item {
				visit(nested)
			}
		case map[string]any:
			if fileID, ok := item["file_id"].(string); ok && fileID != "" {
				width, widthOK := number(item["width"])
				height, heightOK := number(item["height"])
				size, _ := number(item["file_size"])
				area := int64(-1)
				if widthOK && heightOK {
					area = width * height
				}
				if area > bestArea || area == bestArea && size > bestSize {
					bestID, bestArea, bestSize = fileID, area, size
				}
			}
			for _, nested := range item {
				visit(nested)
			}
		}
	}
	visit(payload)
	return bestID
}

func number(value any) (int64, bool) {
	parsed, ok := value.(float64)
	return int64(parsed), ok
}
