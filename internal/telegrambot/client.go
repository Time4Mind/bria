package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
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
	RetryAfter  time.Duration
	Local       bool
}

func (e *APIError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("telegram %s failed with code %d", e.Method, e.Code)
	}
	return fmt.Sprintf("telegram %s failed with code %d: %s", e.Method, e.Code, e.Description)
}

// FloodWait returns Telegram's requested cooldown for a rate-limited call.
// Older Bot API gateways sometimes omit response parameters, so retain a
// bounded description fallback for the canonical "retry after N" response.
func FloodWait(err error) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusTooManyRequests {
		return 0, false
	}
	if apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter, true
	}
	const marker = "retry after "
	description := strings.ToLower(apiErr.Description)
	index := strings.Index(description, marker)
	if index < 0 {
		return 0, true
	}
	var seconds int
	for _, char := range description[index+len(marker):] {
		if char < '0' || char > '9' {
			break
		}
		seconds = seconds*10 + int(char-'0')
	}
	if seconds <= 0 {
		return 0, true
	}
	return time.Duration(seconds) * time.Second, true
}

// RemoteFloodWait distinguishes a Bot API rejection from the local cooldown
// used to suppress repeated edits after that rejection.
func RemoteFloodWait(err error) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Local {
		return 0, false
	}
	return FloodWait(err)
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
		richRequestTimeout = min(requestTimeout, 3*time.Second)
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
	if err := c.callWithConnectionPolicy(
		ctx, "getUpdates", payload, &raw, timeout, true,
	); err != nil {
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
