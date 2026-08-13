package telegrambot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

func (c *Client) GetFile(ctx context.Context, fileID string) (RemoteFile, error) {
	if !validFileID(fileID) {
		return RemoteFile{}, errors.New("invalid Telegram file ID")
	}
	var result apiFile
	if err := c.call(ctx, "getFile", getFilePayload{FileID: fileID}, &result, c.requestTimeout); err != nil {
		return RemoteFile{}, err
	}
	if result.FileID == "" || result.FileID != fileID || result.FileSize < 0 ||
		!safeTelegramFilePath(result.FilePath) {
		return RemoteFile{}, errors.New("telegram returned invalid file metadata")
	}
	return RemoteFile{
		FileID: result.FileID, FileUniqueID: result.FileUniqueID,
		FileSize: result.FileSize, FilePath: result.FilePath,
	}, nil
}

// DownloadFile fetches a Telegram file directly on the consuming node. The
// caller supplies a per-content limit, which may only tighten the hard Bot API
// ceiling. The returned slice is never partially successful.
func (c *Client) DownloadFile(
	ctx context.Context,
	filePath string,
	maxBytes int64,
) ([]byte, error) {
	var destination bytes.Buffer
	_, err := c.downloadFileTo(ctx, filePath, &destination, maxBytes)
	if err != nil {
		return nil, err
	}
	return destination.Bytes(), nil
}

// Download resolves fileID and streams it directly into dst. It implements
// inbound.Downloader without importing the higher-level inbound package.
func (c *Client) Download(
	ctx context.Context,
	fileID string,
	dst io.Writer,
	maxBytes int64,
) (int64, error) {
	if dst == nil || maxBytes <= 0 || maxBytes > MaxTelegramFileBytes {
		return 0, errors.New("invalid Telegram file download")
	}
	file, err := c.GetFile(ctx, fileID)
	if err != nil {
		return 0, err
	}
	if file.FileSize > maxBytes {
		return 0, fileTooLargeError(maxBytes)
	}
	return c.downloadFileTo(ctx, file.FilePath, dst, maxBytes)
}

func (c *Client) downloadFileTo(
	ctx context.Context,
	filePath string,
	dst io.Writer,
	maxBytes int64,
) (int64, error) {
	if dst == nil || !safeTelegramFilePath(filePath) || maxBytes <= 0 ||
		maxBytes > MaxTelegramFileBytes {
		return 0, errors.New("invalid Telegram file download")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.fileRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodGet, c.downloadURL(filePath), nil,
	)
	if err != nil {
		return 0, errors.New("build Telegram file download request")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return 0, requestCtx.Err()
		}
		return 0, &TransportError{Method: "downloadFile", Cause: c.redactedTransportCause(err)}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, &APIError{
			Method: "downloadFile", Code: response.StatusCode,
			Description: http.StatusText(response.StatusCode),
		}
	}
	if response.ContentLength > maxBytes {
		return 0, fileTooLargeError(maxBytes)
	}
	written, err := io.Copy(dst, io.LimitReader(response.Body, maxBytes))
	if err != nil {
		if requestCtx.Err() != nil {
			return written, requestCtx.Err()
		}
		if errors.Is(err, io.ErrShortWrite) {
			return written, &TransportError{Method: "downloadFile", Cause: io.ErrShortWrite}
		}
		return written, &TransportError{
			Method: "downloadFile", Cause: c.redactedTransportCause(err),
		}
	}
	if written == maxBytes {
		var probe [1]byte
		count, probeErr := response.Body.Read(probe[:])
		if count > 0 {
			return written, fileTooLargeError(maxBytes)
		}
		if probeErr != nil && !errors.Is(probeErr, io.EOF) {
			if requestCtx.Err() != nil {
				return written, requestCtx.Err()
			}
			return written, &TransportError{
				Method: "downloadFile", Cause: c.redactedTransportCause(probeErr),
			}
		}
	}
	return written, nil
}

func fileTooLargeError(maxBytes int64) error {
	return fmt.Errorf("%w: limit is %d bytes", ErrTelegramFileTooLarge, maxBytes)
}

func (c *Client) downloadURL(filePath string) string {
	parts := strings.Split(filePath, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return c.baseURL + "/file/bot" + c.token + "/" + strings.Join(parts, "/")
}

func safeTelegramFilePath(value string) bool {
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\\?#\x00\r\n") || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, character := range value {
		if !safeTelegramPathCharacter(character) {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func safeTelegramPathCharacter(character rune) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' ||
		character == '/' || character == '_' || character == '-' || character == '.'
}
