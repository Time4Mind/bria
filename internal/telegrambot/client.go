package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/telegramui"
)

const defaultBaseURL = "https://api.telegram.org"

type ClientConfig struct {
	Token              string
	BaseURL            string
	HTTPClient         HTTPDoer
	ProxyURL           string
	RequestTimeout     time.Duration
	RichRequestTimeout time.Duration
	FileRequestTimeout time.Duration
	LongPollSlack      time.Duration
	MaxResponseBytes   int64
	AllowInsecureHTTP  bool
}

type Client struct {
	token              string
	baseURL            string
	httpClient         HTTPDoer
	requestTimeout     time.Duration
	richRequestTimeout time.Duration
	fileRequestTimeout time.Duration
	longPollSlack      time.Duration
	maxResponseBytes   int64
}

type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("telegram %s failed with code %d", e.Method, e.Code)
	}
	return fmt.Sprintf("telegram %s failed with code %d: %s", e.Method, e.Code, e.Description)
}

type TransportError struct {
	Method string
	Cause  error
}

func (e *TransportError) Error() string { return "telegram " + e.Method + " transport failed" }
func (e *TransportError) Unwrap() error { return e.Cause }

func NewClient(config ClientConfig) (*Client, error) {
	if !validToken(config.Token) {
		return nil, errors.New("telegram bot token is missing or malformed")
	}
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Scheme != "https" &&
		!(config.AllowInsecureHTTP && parsed.Scheme == "http")) {
		return nil, errors.New("telegram base URL must be an absolute HTTPS URL")
	}
	httpClient, err := configuredHTTPClient(config.HTTPClient, config.ProxyURL)
	if err != nil {
		return nil, err
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 10 * time.Second
	}
	richRequestTimeout := config.RichRequestTimeout
	if richRequestTimeout <= 0 || richRequestTimeout > requestTimeout {
		richRequestTimeout = min(requestTimeout, 1200*time.Millisecond)
	}
	fileRequestTimeout := config.FileRequestTimeout
	if fileRequestTimeout <= 0 {
		fileRequestTimeout = 2 * time.Minute
	}
	longPollSlack := config.LongPollSlack
	if longPollSlack <= 0 {
		longPollSlack = 5 * time.Second
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = MaxResponseBytes
	}
	return &Client{
		token: config.Token, baseURL: baseURL, httpClient: httpClient,
		requestTimeout: requestTimeout, richRequestTimeout: richRequestTimeout,
		fileRequestTimeout: fileRequestTimeout,
		longPollSlack:      longPollSlack,
		maxResponseBytes:   maxResponseBytes,
	}, nil
}

func validToken(token string) bool {
	if token == "" || len(token) > 256 {
		return false
	}
	for _, char := range token {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == ':' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func (c *Client) GetUpdates(ctx context.Context, request GetUpdatesRequest) ([]Update, error) {
	if request.Limit == 0 {
		request.Limit = 100
	}
	if request.Limit < 1 || request.Limit > 100 || request.Timeout < 0 || request.Timeout > 50 {
		return nil, errors.New("invalid getUpdates limits")
	}
	payload := getUpdatesPayload{
		Offset: request.Offset, Limit: request.Limit, Timeout: request.Timeout,
		AllowedUpdates: []string{"message", "callback_query"},
	}
	var raw []apiUpdate
	timeout := time.Duration(request.Timeout)*time.Second + c.longPollSlack
	if err := c.call(ctx, "getUpdates", payload, &raw, timeout); err != nil {
		return nil, err
	}
	updates := make([]Update, 0, len(raw))
	for _, item := range raw {
		updates = append(updates, fromAPIUpdate(item))
	}
	return updates, nil
}

func (c *Client) GetMe(ctx context.Context) (BotIdentity, error) {
	var user apiUser
	if err := c.call(ctx, "getMe", struct{}{}, &user, c.requestTimeout); err != nil {
		return BotIdentity{}, err
	}
	if user.ID <= 0 || !user.IsBot || strings.TrimSpace(user.Username) == "" {
		return BotIdentity{}, errors.New("telegram returned an invalid bot identity")
	}
	return BotIdentity{ID: user.ID, Username: user.Username}, nil
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	if id == "" || len([]byte(text)) > MaxCallbackTextBytes {
		return errors.New("invalid callback answer")
	}
	err := c.call(ctx, "answerCallbackQuery", answerCallbackPayload{
		CallbackQueryID: id, Text: text,
	}, nil, c.requestTimeout)
	if isExpiredCallbackError(err) {
		return nil
	}
	return err
}

// SendTyping refreshes Telegram's short-lived typing indicator. Callers must
// repeat it while work is in progress; Telegram clears the action after a few
// seconds or when the bot sends a message.
func (c *Client) SendTyping(ctx context.Context, chatID int64) error {
	if chatID <= 0 {
		return errors.New("invalid chat action target")
	}
	return c.call(ctx, "sendChatAction", sendChatActionPayload{
		ChatID: chatID, Action: "typing",
	}, nil, c.requestTimeout)
}

func (c *Client) SendMessage(ctx context.Context, request MessageRequest) (Message, error) {
	if request.ChatID <= 0 || !validMessageText(request.Text) || !validParseMode(request.ParseMode) {
		return Message{}, errors.New("invalid outgoing message")
	}
	keyboard, err := convertGrid(request.Grid)
	if err != nil {
		return Message{}, err
	}
	var result apiMessageResult
	err = c.call(ctx, "sendMessage", sendMessagePayload{
		ChatID: request.ChatID, Text: request.Text, ParseMode: string(request.ParseMode),
		LinkPreview: linkPreviewOptions{IsDisabled: true}, ReplyMarkup: keyboard,
	}, &result, c.requestTimeout)
	if err != nil {
		return Message{}, err
	}
	return validateMessageResult(result, request.ChatID)
}

func (c *Client) EditMessage(ctx context.Context, request EditMessageRequest) (Message, error) {
	if request.ChatID <= 0 || request.MessageID <= 0 || !validMessageText(request.Text) ||
		!validParseMode(request.ParseMode) {
		return Message{}, errors.New("invalid outgoing message edit")
	}
	keyboard, err := convertGrid(request.Grid)
	if err != nil {
		return Message{}, err
	}
	var result apiMessageResult
	err = c.call(ctx, "editMessageText", editMessagePayload{
		ChatID: request.ChatID, MessageID: request.MessageID,
		Text: request.Text, ParseMode: string(request.ParseMode),
		LinkPreview: linkPreviewOptions{IsDisabled: true}, ReplyMarkup: keyboard,
	}, &result, c.requestTimeout)
	if isUnchangedMessageError(err) {
		return Message{ChatID: request.ChatID, MessageID: request.MessageID}, nil
	}
	if err != nil {
		return Message{}, err
	}
	return validateMessageResult(result, request.ChatID)
}

func isUnchangedMessageError(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(apiErr.Description), "message is not modified")
}

// Telegram callback queries are short-lived. Replaying an update after a
// leader change can therefore make the acknowledgement expire even though the
// update itself still has to be committed to the replicated cursor. Treat the
// terminal acknowledgement as already consumed so it cannot poison polling.
func isExpiredCallbackError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusBadRequest {
		return false
	}
	description := strings.ToLower(apiErr.Description)
	return strings.Contains(description, "query is too old") ||
		strings.Contains(description, "query id is invalid")
}

func (c *Client) DeleteMessage(ctx context.Context, message Message) error {
	if message.ChatID <= 0 || message.MessageID <= 0 {
		return errors.New("invalid outgoing message deletion")
	}
	var deleted bool
	if err := c.call(ctx, "deleteMessage", deleteMessagePayload{
		ChatID: message.ChatID, MessageID: message.MessageID,
	}, &deleted, c.requestTimeout); err != nil {
		return err
	}
	if !deleted {
		return errors.New("Telegram did not delete the message")
	}
	return nil
}

func (c *Client) ClearKeyboard(ctx context.Context, message Message) error {
	if message.ChatID <= 0 || message.MessageID <= 0 {
		return errors.New("invalid message keyboard target")
	}
	var result json.RawMessage
	err := c.call(ctx, "editMessageReplyMarkup", editReplyMarkupPayload{
		ChatID: message.ChatID, MessageID: message.MessageID,
		ReplyMarkup: inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{}},
	}, &result, c.requestTimeout)
	if isUnchangedMessageError(err) {
		return nil
	}
	return err
}

func validMessageText(text string) bool {
	return strings.TrimSpace(text) != "" && len([]byte(text)) <= MaxMessageTextBytes
}

func validParseMode(mode telegramui.ParseMode) bool {
	return mode == "" || mode == telegramui.ParseModeHTML || mode == telegramui.ParseModeMarkdownV2
}

func validateMessageResult(result apiMessageResult, expectedChatID int64) (Message, error) {
	if result.MessageID <= 0 || result.Chat.ID != expectedChatID {
		return Message{}, errors.New("telegram returned an invalid message identity")
	}
	return Message{ChatID: result.Chat.ID, MessageID: result.MessageID}, nil
}

func (c *Client) call(
	ctx context.Context,
	method string,
	payload any,
	result any,
	timeout time.Duration,
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode telegram %s payload: %w", method, err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		c.baseURL+"/bot"+c.token+"/"+method,
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("build telegram %s request: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return requestCtx.Err()
		}
		return &TransportError{Method: method, Cause: c.redactedTransportCause(err)}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, c.maxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return &TransportError{Method: method, Cause: err}
	}
	if int64(len(responseBody)) > c.maxResponseBytes {
		return errors.New("telegram response exceeds configured limit")
	}
	outer := struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code,omitempty"`
		Description string          `json:"description,omitempty"`
	}{}
	if err := json.Unmarshal(responseBody, &outer); err != nil {
		return errors.New("telegram returned malformed JSON")
	}
	if response.StatusCode != http.StatusOK || !outer.OK {
		return &APIError{
			Method: method, Code: outer.ErrorCode,
			Description: boundedDescription(strings.ReplaceAll(
				outer.Description, c.token, "[redacted]",
			)),
		}
	}
	if result == nil {
		return nil
	}
	if len(outer.Result) == 0 || string(outer.Result) == "null" {
		return errors.New("telegram response is missing a result")
	}
	if err := json.Unmarshal(outer.Result, result); err != nil {
		return errors.New("telegram returned a malformed result")
	}
	return nil
}

func boundedDescription(value string) string {
	const max = 256
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}
