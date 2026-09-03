// Package telegram implements the HTTP transport boundary to the Telegram Bot
// API. It contains no product routing or presentation decisions.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	officialBaseEndpoint    = "https://api.telegram.org"
	defaultMaxResponseBytes = int64(1 << 20)
	defaultMaxDownloadBytes = int64(20 << 20)
	defaultMaxUploadBytes   = int64(50 << 20)
	defaultRequestTimeout   = 60 * time.Second
	maxGetUpdatesLimit      = 100
	maxLongPollSeconds      = 50
)

type UpdateID int64
type UserID int64
type ChatID int64
type MessageID int64

// CallbackQueryID is opaque because callback_query.id is a JSON string in the
// Telegram Bot API, unlike numeric update, user, chat, and message IDs.
type CallbackQueryID string

type User struct {
	ID        UserID `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

type Chat struct {
	ID   ChatID `json:"id"`
	Type string `json:"type"`
}

// MessageReference is the non-recursive subset of reply_to_message needed for
// deterministic routing. Telegram does not include a nested reply chain.
type MessageReference struct {
	MessageID MessageID `json:"message_id"`
}

type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Video struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Duration     int    `json:"duration"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Message struct {
	MessageID      MessageID             `json:"message_id"`
	From           *User                 `json:"from,omitempty"`
	Chat           Chat                  `json:"chat"`
	Text           string                `json:"text,omitempty"`
	Caption        string                `json:"caption,omitempty"`
	ReplyToMessage *MessageReference     `json:"reply_to_message,omitempty"`
	Voice          *Voice                `json:"voice,omitempty"`
	Photo          []PhotoSize           `json:"photo,omitempty"`
	Video          *Video                `json:"video,omitempty"`
	Document       *Document             `json:"document,omitempty"`
	ReplyMarkup    *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type CallbackQuery struct {
	ID      CallbackQueryID `json:"id"`
	From    User            `json:"from"`
	Message *Message        `json:"message,omitempty"`
	Data    string          `json:"data,omitempty"`
}

type Update struct {
	UpdateID      UpdateID       `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
	Skipped       bool           `json:"-"`
}

type GetUpdatesRequest struct {
	Offset         UpdateID `json:"offset,omitempty"`
	Limit          int      `json:"limit"`
	TimeoutSeconds int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// BootstrapOffsetRequest makes forgetting Telegram's pending update backlog an
// explicit cutover operation rather than an accidental side effect of startup.
type BootstrapOffsetRequest struct {
	ForgetPendingUpdates bool
}

type SendMessageRequest struct {
	ChatID      ChatID                `json:"chat_id"`
	Text        string                `json:"text"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type EditMessageTextRequest struct {
	ChatID      ChatID                `json:"chat_id"`
	MessageID   MessageID             `json:"message_id"`
	Text        string                `json:"text"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type AnswerCallbackQueryRequest struct {
	CallbackQueryID CallbackQueryID `json:"callback_query_id"`
	Text            string          `json:"text,omitempty"`
}

type DeleteMessageRequest struct {
	ChatID    ChatID    `json:"chat_id"`
	MessageID MessageID `json:"message_id"`
}

// SendDocumentRequest owns immutable content instead of a path or open file.
// Safe file opening and symlink protection remain the responsibility of the
// file layer immediately before it constructs this request.
type SendDocumentRequest struct {
	ChatID      ChatID
	FileName    string
	ContentType string
	Content     []byte
	Caption     string
}

// SendPhotoRequest owns already-rendered image bytes. It deliberately carries
// no local path, caption, reply target, authorization material, or raw prompt.
type SendPhotoRequest struct {
	ChatID      ChatID
	FileName    string
	ContentType string
	Content     []byte
}

// PhotoReceipt is the exact Telegram message receipt for a rendered screen.
type PhotoReceipt struct {
	MessageID MessageID
	ChatID    ChatID
}

type FileReceipt struct {
	MessageID    MessageID
	ChatID       ChatID
	FileID       string
	FileUniqueID string
}

type GetFileRequest struct {
	FileID string `json:"file_id"`
}

// File is Telegram's immutable identity and current short-lived download
// location for one object. FilePath is never accepted directly from a caller.
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path"`
}

type MediaKind string

const (
	MediaVoice MediaKind = "voice"
	MediaPhoto MediaKind = "photo"
	MediaVideo MediaKind = "video"
)

type DownloadMediaRequest struct {
	Kind     MediaKind
	FileID   string
	MaxBytes int64
}

type DownloadedMedia struct {
	File    File
	Content []byte
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Options struct {
	MaxResponseBytes int64
	MaxDownloadBytes int64
	MaxUploadBytes   int64
	RequestTimeout   time.Duration
}

type Client struct {
	httpClient       HTTPClient
	methodURL        func(string) string
	fileURL          func(string) string
	maxResponseBytes int64
	maxDownloadBytes int64
	maxUploadBytes   int64
	requestTimeout   time.Duration
}

type APIError struct {
	Method      string
	HTTPStatus  int
	ErrorCode   int
	Description string
}

func (err *APIError) Error() string {
	return fmt.Sprintf(
		"telegram %s rejected the request (http_status=%d, error_code=%d)",
		err.Method,
		err.HTTPStatus,
		err.ErrorCode,
	)
}

func NewClient(
	token string,
	httpClient HTTPClient,
	options Options,
) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Telegram bot token is required")
	}
	if httpClient == nil {
		return nil, errors.New("Telegram HTTP client is required")
	}
	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if maxResponseBytes < 1 {
		return nil, errors.New("Telegram maximum response size must be positive")
	}
	maxDownloadBytes := options.MaxDownloadBytes
	if maxDownloadBytes == 0 {
		maxDownloadBytes = defaultMaxDownloadBytes
	}
	if maxDownloadBytes < 1 {
		return nil, errors.New("Telegram maximum download size must be positive")
	}
	maxUploadBytes := options.MaxUploadBytes
	if maxUploadBytes == 0 {
		maxUploadBytes = defaultMaxUploadBytes
	}
	if maxUploadBytes < 1 {
		return nil, errors.New("Telegram maximum upload size must be positive")
	}
	requestTimeout := options.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	if requestTimeout < 0 {
		return nil, errors.New("Telegram request timeout must be positive")
	}

	tokenPath := url.PathEscape(token)
	return &Client{
		httpClient: httpClient,
		methodURL: func(method string) string {
			return officialBaseEndpoint + "/bot" + tokenPath + "/" + method
		},
		fileURL: func(filePath string) string {
			return officialBaseEndpoint + "/file/bot" + tokenPath + "/" + escapeFilePath(filePath)
		},
		maxResponseBytes: maxResponseBytes,
		maxDownloadBytes: maxDownloadBytes,
		maxUploadBytes:   maxUploadBytes,
		requestTimeout:   requestTimeout,
	}, nil
}

func (client *Client) String() string { return "telegram.Client" }

func (client *Client) GoString() string { return "&telegram.Client{}" }

// GetMe verifies Bot API reachability and returns the authenticated bot
// identity without consuming or mutating the update queue.
func (client *Client) GetMe(ctx context.Context) (User, error) {
	var identity User
	if err := client.call(ctx, "getMe", struct{}{}, &identity); err != nil {
		return User{}, err
	}
	if identity.ID <= 0 || !identity.IsBot {
		return User{}, errors.New("Telegram getMe returned an invalid bot identity")
	}
	return identity, nil
}

// DownloadMedia resolves and downloads only voice and photo inputs. Video is
// rejected before getFile so a future caller cannot accidentally fetch it.
// The returned byte slice has been fully read under both the configured cap
// and the narrower per-call cap.
func (client *Client) DownloadMedia(
	ctx context.Context,
	request DownloadMediaRequest,
) (DownloadedMedia, error) {
	if request.Kind == MediaVideo {
		return DownloadedMedia{}, ErrVideoDownloadForbidden
	}
	if request.Kind != MediaVoice && request.Kind != MediaPhoto {
		return DownloadedMedia{}, errors.New("Telegram media download kind must be voice or photo")
	}
	if request.FileID == "" || strings.TrimSpace(request.FileID) != request.FileID {
		return DownloadedMedia{}, errors.New("Telegram media file id is required")
	}
	maxBytes := request.MaxBytes
	if maxBytes == 0 {
		maxBytes = client.maxDownloadBytes
	}
	if maxBytes < 1 || maxBytes > client.maxDownloadBytes {
		return DownloadedMedia{}, errors.New("Telegram media download limit is outside the configured bound")
	}

	file, err := client.GetFile(ctx, GetFileRequest{FileID: request.FileID})
	if err != nil {
		return DownloadedMedia{}, err
	}
	if file.FileSize > maxBytes {
		return DownloadedMedia{}, fmt.Errorf("Telegram media file exceeds %d bytes", maxBytes)
	}
	content, err := client.downloadFile(ctx, file.FilePath, maxBytes)
	if err != nil {
		return DownloadedMedia{}, err
	}
	return DownloadedMedia{File: file, Content: content}, nil
}

func (client *Client) GetFile(ctx context.Context, request GetFileRequest) (File, error) {
	if request.FileID == "" || strings.TrimSpace(request.FileID) != request.FileID {
		return File{}, errors.New("Telegram getFile file id is required")
	}
	var file File
	if err := client.call(ctx, "getFile", request, &file); err != nil {
		return File{}, err
	}
	if file.FileID != request.FileID || strings.TrimSpace(file.FileUniqueID) == "" || file.FileSize < 0 {
		return File{}, errors.New("Telegram getFile returned invalid file metadata")
	}
	if err := validateFilePath(file.FilePath); err != nil {
		return File{}, err
	}
	return file, nil
}

func (client *Client) GetUpdates(
	ctx context.Context,
	request GetUpdatesRequest,
) ([]Update, error) {
	if request.Limit < 1 || request.Limit > maxGetUpdatesLimit {
		return nil, fmt.Errorf("Telegram update limit must be between 1 and %d", maxGetUpdatesLimit)
	}
	if request.TimeoutSeconds < 0 || request.TimeoutSeconds > maxLongPollSeconds {
		return nil, fmt.Errorf(
			"Telegram poll timeout must be between 0 and %d seconds",
			maxLongPollSeconds,
		)
	}
	for _, updateType := range request.AllowedUpdates {
		if strings.TrimSpace(updateType) == "" {
			return nil, errors.New("Telegram allowed update type must not be empty")
		}
	}

	var rawUpdates []json.RawMessage
	if err := client.call(ctx, "getUpdates", request, &rawUpdates); err != nil {
		return nil, err
	}
	updates := make([]Update, 0, len(rawUpdates))
	for index, rawUpdate := range rawUpdates {
		updateID, err := decodeUpdateID(rawUpdate)
		if err != nil {
			return nil, fmt.Errorf("normalize Telegram update at index %d: %w", index, err)
		}
		var update Update
		if err := json.Unmarshal(rawUpdate, &update); err != nil {
			updates = append(updates, Update{UpdateID: updateID, Skipped: true})
			continue
		}
		if err := validateUpdate(update); err != nil {
			updates = append(updates, Update{UpdateID: updateID, Skipped: true})
			continue
		}
		updates = append(updates, update)
	}
	return updates, nil
}

// BootstrapOffset forgets the pre-cutover backlog with Telegram's offset=-1
// short-poll contract and returns the first offset safe for live dispatch. It
// never returns the fetched stale payload to the caller.
func (client *Client) BootstrapOffset(
	ctx context.Context,
	request BootstrapOffsetRequest,
) (UpdateID, error) {
	if !request.ForgetPendingUpdates {
		return 0, errors.New("Telegram bootstrap requires explicit permission to forget pending updates")
	}
	updates, err := client.GetUpdates(ctx, GetUpdatesRequest{
		Offset:         -1,
		Limit:          1,
		TimeoutSeconds: 0,
	})
	if err != nil {
		return 0, err
	}
	if len(updates) == 0 {
		return 0, nil
	}
	const maxUpdateID = UpdateID(1<<63 - 1)
	if updates[0].UpdateID == maxUpdateID {
		return 0, errors.New("Telegram bootstrap update id cannot be advanced")
	}
	return updates[0].UpdateID + 1, nil
}

func (client *Client) SendMessage(
	ctx context.Context,
	request SendMessageRequest,
) (Message, error) {
	if request.ChatID == 0 {
		return Message{}, errors.New("Telegram send chat id is required")
	}
	if strings.TrimSpace(request.Text) == "" {
		return Message{}, errors.New("Telegram send text is required")
	}
	if err := validateInlineKeyboard(request.ReplyMarkup); err != nil {
		return Message{}, fmt.Errorf("Telegram send reply markup: %w", err)
	}
	var message Message
	if err := client.call(ctx, "sendMessage", request, &message); err != nil {
		return Message{}, err
	}
	if err := validateMessage(message); err != nil {
		return Message{}, fmt.Errorf("normalize Telegram sent message: %w", err)
	}
	return message, nil
}

// SendDocument performs exactly one multipart Bot API attempt. Any outcome
// without a valid Telegram receipt is classified as unknown unless Telegram
// returned a definitive API rejection; callers must not retry automatically.
func (client *Client) SendDocument(
	ctx context.Context,
	request SendDocumentRequest,
) (FileReceipt, error) {
	if request.ChatID == 0 {
		return FileReceipt{}, errors.New("Telegram document chat id is required")
	}
	if err := validateUploadFileName(request.FileName); err != nil {
		return FileReceipt{}, err
	}
	if len(request.Content) == 0 {
		return FileReceipt{}, errors.New("Telegram document content is required")
	}
	if int64(len(request.Content)) > client.maxUploadBytes {
		return FileReceipt{}, fmt.Errorf("Telegram document exceeds %d bytes", client.maxUploadBytes)
	}
	if strings.ContainsRune(request.Caption, '\x00') {
		return FileReceipt{}, errors.New("Telegram document caption contains an invalid character")
	}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(int64(request.ChatID), 10)); err != nil {
		return FileReceipt{}, errors.New("encode Telegram sendDocument request")
	}
	if request.Caption != "" {
		if err := writer.WriteField("caption", request.Caption); err != nil {
			return FileReceipt{}, errors.New("encode Telegram sendDocument request")
		}
	}
	header := make(textproto.MIMEHeader)
	disposition := mime.FormatMediaType("form-data", map[string]string{
		"name": "document", "filename": request.FileName,
	})
	if disposition == "" {
		return FileReceipt{}, errors.New("encode Telegram sendDocument filename")
	}
	header.Set("Content-Disposition", disposition)
	contentType := strings.TrimSpace(request.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		return FileReceipt{}, errors.New("Telegram document content type is invalid")
	}
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return FileReceipt{}, errors.New("encode Telegram sendDocument request")
	}
	if _, err := part.Write(request.Content); err != nil {
		return FileReceipt{}, errors.New("encode Telegram sendDocument request")
	}
	if err := writer.Close(); err != nil {
		return FileReceipt{}, errors.New("encode Telegram sendDocument request")
	}

	var message Message
	if err := client.callMultipart(ctx, "sendDocument", writer.FormDataContentType(), body.Bytes(), &message); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && isDefinitiveDeliveryRejection(apiErr) {
			return FileReceipt{}, err
		}
		return FileReceipt{}, fmt.Errorf("%w: %w", ErrDeliveryUnknown, err)
	}
	if err := validateMessage(message); err != nil {
		return FileReceipt{}, fmt.Errorf("%w: Telegram sendDocument returned an invalid message", ErrDeliveryUnknown)
	}
	if message.Chat.ID != request.ChatID {
		return FileReceipt{}, fmt.Errorf("%w: Telegram sendDocument returned a receipt for another chat", ErrDeliveryUnknown)
	}
	if message.Document == nil || strings.TrimSpace(message.Document.FileID) == "" ||
		strings.TrimSpace(message.Document.FileUniqueID) == "" {
		return FileReceipt{}, fmt.Errorf("%w: Telegram sendDocument returned no file receipt", ErrDeliveryUnknown)
	}
	return FileReceipt{
		MessageID: message.MessageID, ChatID: message.Chat.ID, FileID: message.Document.FileID,
		FileUniqueID: message.Document.FileUniqueID,
	}, nil
}

// SendPhoto performs exactly one multipart Bot API attempt. An absent or
// mismatched ChatID/MessageID receipt is never treated as a confirmed screen.
func (client *Client) SendPhoto(ctx context.Context, request SendPhotoRequest) (PhotoReceipt, error) {
	if request.ChatID == 0 {
		return PhotoReceipt{}, errors.New("Telegram photo chat id is required")
	}
	if err := validateUploadFileName(request.FileName); err != nil {
		return PhotoReceipt{}, err
	}
	if len(request.Content) == 0 {
		return PhotoReceipt{}, errors.New("Telegram photo content is required")
	}
	if int64(len(request.Content)) > client.maxUploadBytes {
		return PhotoReceipt{}, fmt.Errorf("Telegram photo exceeds %d bytes", client.maxUploadBytes)
	}
	contentType := strings.TrimSpace(request.ContentType)
	if contentType == "" {
		contentType = "image/png"
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err != nil || !strings.HasPrefix(mediaType, "image/") {
		return PhotoReceipt{}, errors.New("Telegram photo content type is invalid")
	}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(int64(request.ChatID), 10)); err != nil {
		return PhotoReceipt{}, errors.New("encode Telegram sendPhoto request")
	}
	header := make(textproto.MIMEHeader)
	disposition := mime.FormatMediaType("form-data", map[string]string{"name": "photo", "filename": request.FileName})
	if disposition == "" {
		return PhotoReceipt{}, errors.New("encode Telegram sendPhoto filename")
	}
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return PhotoReceipt{}, errors.New("encode Telegram sendPhoto request")
	}
	if _, err := part.Write(request.Content); err != nil {
		return PhotoReceipt{}, errors.New("encode Telegram sendPhoto request")
	}
	if err := writer.Close(); err != nil {
		return PhotoReceipt{}, errors.New("encode Telegram sendPhoto request")
	}
	var message Message
	if err := client.callMultipart(ctx, "sendPhoto", writer.FormDataContentType(), body.Bytes(), &message); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && isDefinitiveDeliveryRejection(apiErr) {
			return PhotoReceipt{}, err
		}
		return PhotoReceipt{}, fmt.Errorf("%w: %w", ErrDeliveryUnknown, err)
	}
	if err := validateMessage(message); err != nil || message.MessageID <= 0 || message.Chat.ID != request.ChatID || len(message.Photo) == 0 {
		return PhotoReceipt{}, fmt.Errorf("%w: Telegram sendPhoto returned an invalid receipt", ErrDeliveryUnknown)
	}
	return PhotoReceipt{MessageID: message.MessageID, ChatID: message.Chat.ID}, nil
}

func (client *Client) EditMessageText(
	ctx context.Context,
	request EditMessageTextRequest,
) (Message, error) {
	if request.ChatID == 0 {
		return Message{}, errors.New("Telegram edit chat id is required")
	}
	if request.MessageID <= 0 {
		return Message{}, errors.New("Telegram edit message id is required")
	}
	if strings.TrimSpace(request.Text) == "" {
		return Message{}, errors.New("Telegram edit text is required")
	}
	if err := validateInlineKeyboard(request.ReplyMarkup); err != nil {
		return Message{}, fmt.Errorf("Telegram edit reply markup: %w", err)
	}
	var message Message
	if err := client.call(ctx, "editMessageText", request, &message); err != nil {
		return Message{}, err
	}
	if err := validateMessage(message); err != nil {
		return Message{}, fmt.Errorf("normalize Telegram edited message: %w", err)
	}
	return message, nil
}

func (client *Client) AnswerCallbackQuery(
	ctx context.Context,
	request AnswerCallbackQueryRequest,
) error {
	if strings.TrimSpace(string(request.CallbackQueryID)) == "" {
		return errors.New("Telegram callback query id is required")
	}
	var accepted bool
	if err := client.call(ctx, "answerCallbackQuery", request, &accepted); err != nil {
		return err
	}
	if !accepted {
		return errors.New("Telegram callback answer was not accepted")
	}
	return nil
}

// DeleteMessage is the narrow Bot API primitive used to remove an inbound
// authorization code or secret after local processing.
func (client *Client) DeleteMessage(
	ctx context.Context,
	request DeleteMessageRequest,
) error {
	if request.ChatID == 0 {
		return errors.New("Telegram delete chat id is required")
	}
	if request.MessageID <= 0 {
		return errors.New("Telegram delete message id is required")
	}
	var deleted bool
	if err := client.call(ctx, "deleteMessage", request, &deleted); err != nil {
		return err
	}
	if !deleted {
		return errors.New("Telegram message deletion was not accepted")
	}
	return nil
}

type apiEnvelope struct {
	OK          *bool           `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

var errResponseTooLarge = errors.New("Telegram response too large")

var ErrVideoDownloadForbidden = errors.New("Telegram video download is forbidden")

// ErrDeliveryUnknown means a mutating Telegram request may have been applied
// but no valid receipt was obtained. Automatic retry can duplicate delivery.
var ErrDeliveryUnknown = errors.New("Telegram delivery outcome is unknown")

func IsDeliveryUnknown(err error) bool { return errors.Is(err, ErrDeliveryUnknown) }

// ErrTransient marks a Telegram failure that is safe to retry because no
// valid protocol response was received or Telegram explicitly requested a
// later attempt. It never classifies authentication or polling conflicts.
var ErrTransient = errors.New("transient Telegram failure")

// IsTransient reports whether a request can be retried without converting an
// authentication, concurrent-poller, or protocol failure into a crash loop.
func IsTransient(err error) bool {
	if errors.Is(err, ErrTransient) {
		return true
	}
	var apiError *APIError
	if !errors.As(err, &apiError) {
		return false
	}
	if isPermanentAPIStatus(apiError.HTTPStatus) || isPermanentAPIStatus(apiError.ErrorCode) {
		return false
	}
	return apiError.HTTPStatus == http.StatusRequestTimeout ||
		apiError.HTTPStatus == http.StatusTooManyRequests ||
		apiError.HTTPStatus >= http.StatusInternalServerError ||
		apiError.ErrorCode == http.StatusTooManyRequests ||
		apiError.ErrorCode >= http.StatusInternalServerError
}

func (client *Client) call(
	ctx context.Context,
	method string,
	payload any,
	result any,
) error {
	if ctx == nil {
		return errors.New("Telegram request context is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Telegram %s request", method)
	}
	requestCtx, cancel := context.WithTimeout(ctx, client.requestTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		client.methodURL(method),
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create Telegram %s request", method)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if httpResponse != nil && httpResponse.Body != nil {
			_ = httpResponse.Body.Close()
		}
		return safeRequestError(method, ctx, requestCtx, err)
	}
	if httpResponse == nil || httpResponse.Body == nil {
		return fmt.Errorf("telegram %s returned no response", method)
	}
	defer httpResponse.Body.Close()

	responseBody, err := readBounded(httpResponse.Body, client.maxResponseBytes)
	if err != nil {
		if !errors.Is(err, errResponseTooLarge) {
			if isPermanentAPIStatus(httpResponse.StatusCode) {
				return &APIError{Method: method, HTTPStatus: httpResponse.StatusCode}
			}
			return fmt.Errorf("%w: telegram %s response could not be read", ErrTransient, method)
		}
		return fmt.Errorf(
			"telegram %s response exceeded %d bytes",
			method,
			client.maxResponseBytes,
		)
	}
	envelope, err := decodeEnvelope(responseBody)
	if err != nil {
		return fmt.Errorf("telegram %s returned an invalid JSON envelope", method)
	}
	if envelope.OK == nil {
		return fmt.Errorf("telegram %s response omitted ok", method)
	}
	if !*envelope.OK || httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return &APIError{
			Method:      method,
			HTTPStatus:  httpResponse.StatusCode,
			ErrorCode:   envelope.ErrorCode,
			Description: envelope.Description,
		}
	}
	if len(envelope.Result) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Result), []byte("null")) {
		return fmt.Errorf("telegram %s response omitted result", method)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("telegram %s returned an invalid result", method)
	}
	return nil
}

func (client *Client) callMultipart(
	ctx context.Context,
	method string,
	contentType string,
	body []byte,
	result any,
) error {
	if ctx == nil {
		return errors.New("Telegram request context is required")
	}
	requestCtx, cancel := context.WithTimeout(ctx, client.requestTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		client.methodURL(method),
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create Telegram %s request", method)
	}
	httpRequest.Header.Set("Content-Type", contentType)
	httpResponse, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if httpResponse != nil && httpResponse.Body != nil {
			_ = httpResponse.Body.Close()
		}
		return safeRequestError(method, ctx, requestCtx, err)
	}
	if httpResponse == nil || httpResponse.Body == nil {
		return fmt.Errorf("telegram %s returned no response", method)
	}
	defer httpResponse.Body.Close()

	responseBody, err := readBounded(httpResponse.Body, client.maxResponseBytes)
	if err != nil {
		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			return &APIError{Method: method, HTTPStatus: httpResponse.StatusCode}
		}
		return fmt.Errorf("telegram %s response could not be read", method)
	}
	envelope, err := decodeEnvelope(responseBody)
	if err != nil || envelope.OK == nil {
		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			return &APIError{Method: method, HTTPStatus: httpResponse.StatusCode}
		}
		return fmt.Errorf("telegram %s returned an invalid JSON envelope", method)
	}
	if !*envelope.OK || httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return &APIError{
			Method: method, HTTPStatus: httpResponse.StatusCode,
			ErrorCode: envelope.ErrorCode, Description: envelope.Description,
		}
	}
	if len(envelope.Result) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Result), []byte("null")) {
		return fmt.Errorf("telegram %s response omitted result", method)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("telegram %s returned an invalid result", method)
	}
	return nil
}

func (client *Client) downloadFile(
	ctx context.Context,
	filePath string,
	maxBytes int64,
) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("Telegram request context is required")
	}
	if err := validateFilePath(filePath); err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, client.requestTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		client.fileURL(filePath),
		nil,
	)
	if err != nil {
		return nil, errors.New("create Telegram downloadFile request")
	}
	httpResponse, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if httpResponse != nil && httpResponse.Body != nil {
			_ = httpResponse.Body.Close()
		}
		return nil, safeRequestError("downloadFile", ctx, requestCtx, err)
	}
	if httpResponse == nil || httpResponse.Body == nil {
		return nil, errors.New("telegram downloadFile returned no response")
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return nil, &APIError{Method: "downloadFile", HTTPStatus: httpResponse.StatusCode}
	}
	contentLength := httpResponse.ContentLength
	if contentLength < 0 {
		if header := httpResponse.Header.Get("Content-Length"); header != "" {
			if parsed, parseErr := strconv.ParseInt(header, 10, 64); parseErr == nil {
				contentLength = parsed
			}
		}
	}
	if contentLength > maxBytes {
		return nil, fmt.Errorf("Telegram media file exceeds %d bytes", maxBytes)
	}
	content, err := readBounded(httpResponse.Body, maxBytes)
	if err != nil {
		if errors.Is(err, errResponseTooLarge) {
			return nil, fmt.Errorf("Telegram media file exceeds %d bytes", maxBytes)
		}
		return nil, fmt.Errorf("%w: telegram downloadFile response could not be read", ErrTransient)
	}
	return content, nil
}

func isPermanentAPIStatus(status int) bool {
	return status == http.StatusUnauthorized ||
		status == http.StatusForbidden ||
		status == http.StatusConflict
}

func isDefinitiveDeliveryRejection(err *APIError) bool {
	for _, status := range []int{err.HTTPStatus, err.ErrorCode} {
		if status >= 400 && status < 500 && status != http.StatusRequestTimeout {
			return true
		}
	}
	return false
}

func safeRequestError(
	method string,
	callerContext context.Context,
	requestContext context.Context,
	requestErr error,
) error {
	if ctxErr := callerContext.Err(); ctxErr != nil {
		return fmt.Errorf("telegram %s request: %w", method, ctxErr)
	}
	if errors.Is(requestContext.Err(), context.DeadlineExceeded) ||
		errors.Is(requestErr, context.DeadlineExceeded) {
		return fmt.Errorf(
			"%w: telegram %s request: %w",
			ErrTransient,
			method,
			context.DeadlineExceeded,
		)
	}
	if errors.Is(requestErr, context.Canceled) {
		return fmt.Errorf("%w: telegram %s request failed", ErrTransient, method)
	}
	return fmt.Errorf("%w: telegram %s request failed", ErrTransient, method)
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errResponseTooLarge
	}
	return body, nil
}

func validateFilePath(filePath string) error {
	if filePath == "" || len(filePath) > 1024 || strings.TrimSpace(filePath) != filePath ||
		strings.HasPrefix(filePath, "/") || strings.ContainsAny(filePath, "\\?#\x00") ||
		path.Clean(filePath) != filePath {
		return errors.New("Telegram getFile returned an unsafe file path")
	}
	for _, segment := range strings.Split(filePath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("Telegram getFile returned an unsafe file path")
		}
	}
	return nil
}

func escapeFilePath(filePath string) string {
	segments := strings.Split(filePath, "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	return strings.Join(segments, "/")
}

func validateUploadFileName(fileName string) error {
	if fileName == "" || len(fileName) > 255 || strings.TrimSpace(fileName) != fileName ||
		fileName == "." || fileName == ".." || strings.ContainsAny(fileName, "/\\\r\n\x00") {
		return errors.New("Telegram document filename must be a safe base name")
	}
	return nil
}

func decodeEnvelope(body []byte) (apiEnvelope, error) {
	var envelope apiEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil {
		return apiEnvelope{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return apiEnvelope{}, errors.New("trailing JSON value")
	}
	return envelope, nil
}

func validateUpdate(update Update) error {
	if update.UpdateID <= 0 {
		return errors.New("update_id must be positive")
	}
	supportedPayloads := 0
	if update.Message != nil {
		supportedPayloads++
		if err := validateMessage(*update.Message); err != nil {
			return fmt.Errorf("message: %w", err)
		}
	}
	if update.CallbackQuery != nil {
		supportedPayloads++
		if err := validateCallbackQuery(*update.CallbackQuery); err != nil {
			return fmt.Errorf("callback_query: %w", err)
		}
	}
	if supportedPayloads != 1 {
		return errors.New("exactly one supported update payload is required")
	}
	return nil
}

func decodeUpdateID(rawUpdate json.RawMessage) (UpdateID, error) {
	var identity struct {
		UpdateID *UpdateID `json:"update_id"`
	}
	if err := json.Unmarshal(rawUpdate, &identity); err != nil {
		return 0, errors.New("update must be a JSON object with a numeric update_id")
	}
	if identity.UpdateID == nil || *identity.UpdateID <= 0 {
		return 0, errors.New("update_id must be positive")
	}
	return *identity.UpdateID, nil
}

func validateMessage(message Message) error {
	if message.MessageID <= 0 {
		return errors.New("message_id must be positive")
	}
	if message.Chat.ID == 0 || strings.TrimSpace(message.Chat.Type) == "" {
		return errors.New("chat id and type are required")
	}
	if message.From == nil || message.From.ID <= 0 {
		return errors.New("sender id is required")
	}
	if message.ReplyToMessage != nil && message.ReplyToMessage.MessageID <= 0 {
		return errors.New("reply_to_message id must be positive")
	}
	mediaPayloads := 0
	if message.Voice != nil {
		mediaPayloads++
		if !validFileIdentity(message.Voice.FileID, message.Voice.FileUniqueID) ||
			message.Voice.Duration < 0 || message.Voice.FileSize < 0 {
			return errors.New("voice metadata is invalid")
		}
	}
	if len(message.Photo) > 0 {
		mediaPayloads++
		for _, photo := range message.Photo {
			if !validFileIdentity(photo.FileID, photo.FileUniqueID) ||
				photo.Width <= 0 || photo.Height <= 0 || photo.Width > 1<<20 || photo.Height > 1<<20 ||
				photo.FileSize < 0 {
				return errors.New("photo metadata is invalid")
			}
		}
	}
	if message.Video != nil {
		mediaPayloads++
		if !validFileIdentity(message.Video.FileID, message.Video.FileUniqueID) ||
			message.Video.Width <= 0 || message.Video.Height <= 0 ||
			message.Video.Width > 1<<20 || message.Video.Height > 1<<20 ||
			message.Video.Duration < 0 || message.Video.FileSize < 0 {
			return errors.New("video metadata is invalid")
		}
	}
	if message.Document != nil {
		mediaPayloads++
	}
	if mediaPayloads > 1 {
		return errors.New("message contains multiple media payloads")
	}
	return nil
}

func validFileIdentity(fileID, uniqueID string) bool {
	return fileID != "" && strings.TrimSpace(fileID) == fileID &&
		uniqueID != "" && strings.TrimSpace(uniqueID) == uniqueID
}

func validateCallbackQuery(callback CallbackQuery) error {
	if strings.TrimSpace(string(callback.ID)) == "" {
		return errors.New("callback query id is required")
	}
	if callback.From.ID <= 0 {
		return errors.New("callback sender id is required")
	}
	if callback.Message == nil {
		return errors.New("callback message is required")
	}
	if err := validateMessage(*callback.Message); err != nil {
		return err
	}
	if strings.TrimSpace(callback.Data) == "" {
		return errors.New("callback data is required")
	}
	return nil
}

func validateInlineKeyboard(markup *InlineKeyboardMarkup) error {
	if markup == nil {
		return nil
	}
	for rowIndex, row := range markup.InlineKeyboard {
		if len(row) == 0 {
			return fmt.Errorf("inline keyboard row %d must not be empty", rowIndex)
		}
		for buttonIndex, button := range row {
			if strings.TrimSpace(button.Text) == "" {
				return fmt.Errorf("inline keyboard button %d:%d text is required", rowIndex, buttonIndex)
			}
			if size := len(button.CallbackData); size < 1 || size > 64 {
				return fmt.Errorf("inline keyboard button %d:%d callback data must be 1..64 bytes", rowIndex, buttonIndex)
			}
		}
	}
	return nil
}
