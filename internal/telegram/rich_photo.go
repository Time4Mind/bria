package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"mime/multipart"
	"strconv"
	"strings"
)

const (
	ScreenPhotoID     = "terminal_screenshot"
	MaxRichPhotoBytes = 1 << 20
)

type InputRichMessageMedia struct {
	ID    string              `json:"id"`
	Media InputRichMediaPhoto `json:"media"`
}
type InputRichMediaPhoto struct {
	Type  string `json:"type"`
	Media string `json:"media"`
}

func validateRichPhoto(rich InputRichMessage, data []byte) error {
	validShell := len(rich.Media) == 1 && rich.Media[0].ID == ScreenPhotoID &&
		rich.Media[0].Media.Type == "photo" &&
		strings.Contains(rich.Markdown, "tg://photo?id="+ScreenPhotoID)
	if len(data) == 0 {
		if len(rich.Media) == 0 {
			return nil
		}
		if !validShell || !validRichPhotoFileID(rich.Media[0].Media.Media) {
			return errors.New("Telegram rich photo upload is missing")
		}
		return nil
	}
	if len(data) > MaxRichPhotoBytes || !validShell || rich.Media[0].Media.Media != "attach://"+ScreenPhotoID {
		return errors.New("Telegram rich photo is invalid")
	}
	dimensions, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || dimensions.Width <= 0 || dimensions.Height <= 0 || dimensions.Width > 2000 || dimensions.Height > 2000 {
		return errors.New("Telegram rich PNG is invalid or oversized")
	}
	return nil
}

// RichPhotoFileID returns Telegram's reusable identity for the largest rich
// photo in a confirmed message receipt. Rich media is nested in rich_message
// on Bot API versions which support sendRichMessage/editMessageText.
func RichPhotoFileID(message Message) string {
	bestID, bestArea, bestSize := largestReusablePhoto(message.Photo)
	if len(message.RichMessage) == 0 {
		return bestID
	}
	var payload any
	if json.Unmarshal(message.RichMessage, &payload) != nil {
		return bestID
	}
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case []any:
			for _, nested := range item {
				visit(nested)
			}
		case map[string]any:
			if candidate, ok := item["file_id"].(string); ok && validRichPhotoFileID(candidate) {
				width, widthOK := jsonNumber(item["width"])
				height, heightOK := jsonNumber(item["height"])
				size, _ := jsonNumber(item["file_size"])
				area := int64(-1)
				if widthOK && heightOK && width > 0 && height > 0 {
					area = width * height
				}
				if area > bestArea || area == bestArea && size > bestSize {
					bestID, bestArea, bestSize = candidate, area, size
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

func largestReusablePhoto(photos []PhotoSize) (string, int64, int64) {
	bestID, bestArea, bestSize := "", int64(-1), int64(-1)
	for _, photo := range photos {
		if !validRichPhotoFileID(photo.FileID) || photo.Width <= 0 || photo.Height <= 0 {
			continue
		}
		area := int64(photo.Width) * int64(photo.Height)
		if area > bestArea || area == bestArea && photo.FileSize > bestSize {
			bestID, bestArea, bestSize = photo.FileID, area, photo.FileSize
		}
	}
	return bestID, bestArea, bestSize
}

func jsonNumber(value any) (int64, bool) {
	number, ok := value.(float64)
	return int64(number), ok
}

func validRichPhotoFileID(value string) bool {
	if value == "" || len(value) > 1024 || strings.HasPrefix(value, "attach://") {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func (client *Client) callRichPhoto(ctx context.Context, method string, chat ChatID, messageID MessageID, rich InputRichMessage, keyboard *InlineKeyboardMarkup, priority MutationPriority, data []byte, result any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{"chat_id": strconv.FormatInt(int64(chat), 10)}
	if messageID != 0 {
		fields["message_id"] = strconv.FormatInt(int64(messageID), 10)
	}
	encoded, err := json.Marshal(rich)
	if err != nil {
		return err
	}
	fields["rich_message"] = string(encoded)
	if keyboard != nil {
		encoded, err = json.Marshal(keyboard)
		if err != nil {
			return err
		}
		fields["reply_markup"] = string(encoded)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile(ScreenPhotoID, "terminal_screenshot.png")
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.callMultipart(ctx, Mutation{Method: method, ChatID: int64(chat), CardID: int64(messageID), Priority: priority, Heavy: true}, writer.FormDataContentType(), body.Bytes(), result)
}
