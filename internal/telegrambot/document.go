package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strconv"
)

type DocumentRequest struct {
	ChatID   int64
	Name     string
	MIMEType string
	Size     int64
	Content  io.Reader
	Caption  string
}

func (c *Client) SendDocument(ctx context.Context, request DocumentRequest) (Message, error) {
	if request.ChatID <= 0 || request.Content == nil || request.Size < 0 ||
		request.Size > 45<<20 || filepath.Base(request.Name) != request.Name || request.Name == "" ||
		len([]byte(request.Caption)) > 1024 {
		return Message{}, errors.New("invalid outgoing document")
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		err := multipartWriter.WriteField("chat_id", strconv.FormatInt(request.ChatID, 10))
		if err == nil && request.Caption != "" {
			err = multipartWriter.WriteField("caption", request.Caption)
		}
		if err == nil {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", fmt.Sprintf(
				`form-data; name="document"; filename=%q`, request.Name,
			))
			header.Set("Content-Type", request.MIMEType)
			var part io.Writer
			part, err = multipartWriter.CreatePart(header)
			if err == nil {
				_, err = io.CopyN(part, request.Content, request.Size)
			}
		}
		if closeErr := multipartWriter.Close(); err == nil {
			err = closeErr
		}
		_ = writer.CloseWithError(err)
	}()
	requestCtx, cancel := context.WithTimeout(ctx, c.fileRequestTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx, http.MethodPost, c.baseURL+"/bot"+c.token+"/sendDocument", reader,
	)
	if err != nil {
		_ = reader.CloseWithError(err)
		return Message{}, err
	}
	httpRequest.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		_ = reader.CloseWithError(err)
		return Message{}, &TransportError{Method: "sendDocument", Cause: c.redactedTransportCause(err)}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, c.maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil || int64(len(body)) > c.maxResponseBytes {
		return Message{}, errors.New("invalid Telegram document response")
	}
	var envelope apiEnvelope[apiMessageResult]
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Message{}, errors.New("Telegram returned malformed JSON")
	}
	if response.StatusCode != http.StatusOK || !envelope.OK {
		return Message{}, &APIError{
			Method: "sendDocument", Code: envelope.ErrorCode,
			Description: boundedDescription(envelope.Description),
		}
	}
	return validateMessageResult(envelope.Result, request.ChatID)
}
