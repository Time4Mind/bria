package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

func (c *Client) callRichMultipart(
	ctx context.Context,
	method string,
	chatID int64,
	messageID int64,
	rich richMessage,
	keyboard inlineKeyboardMarkup,
	pngBytes []byte,
	timeout time.Duration,
) (json.RawMessage, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	richJSON, err := marshalField(rich)
	if err != nil {
		return nil, fmt.Errorf("encode rich message: %w", err)
	}
	keyboardJSON, err := marshalField(keyboard)
	if err != nil {
		return nil, fmt.Errorf("encode rich keyboard: %w", err)
	}
	fields := map[string]string{
		"chat_id": strconv.FormatInt(chatID, 10), "rich_message": richJSON,
		"reply_markup": keyboardJSON,
	}
	if messageID > 0 {
		fields["message_id"] = strconv.FormatInt(messageID, 10)
	} else {
		fields["disable_notification"] = "true"
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return nil, fmt.Errorf("encode rich field %s: %w", name, err)
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+richPhotoID+`"; filename="`+richPhotoFilename+`"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, fmt.Errorf("encode rich pane file: %w", err)
	}
	if _, err := part.Write(pngBytes); err != nil {
		return nil, fmt.Errorf("encode rich pane bytes: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish rich multipart: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, c.baseURL+"/bot"+c.token+"/"+method, &body,
	)
	if err != nil {
		return nil, fmt.Errorf("build telegram %s request: %w", method, err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.httpClient.Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return nil, requestCtx.Err()
		}
		return nil, &TransportError{Method: method, Cause: err}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, c.maxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, &TransportError{Method: method, Cause: err}
	}
	if int64(len(responseBody)) > c.maxResponseBytes {
		return nil, errors.New("Telegram response exceeds configured limit")
	}
	var envelope apiEnvelope[json.RawMessage]
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, errors.New("Telegram returned malformed JSON")
	}
	if response.StatusCode != http.StatusOK || !envelope.OK {
		return nil, &APIError{
			Method: method, Code: envelope.ErrorCode,
			Description: boundedDescription(strings.ReplaceAll(envelope.Description, c.token, "[redacted]")),
		}
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, errors.New("Telegram rich response is missing a result")
	}
	return envelope.Result, nil
}
