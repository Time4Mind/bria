// Package telegrambot implements the Telegram Bot API transport boundary.
//
// It deliberately does not route application commands. Incoming updates are
// reduced to private-DM transport events and outgoing telegramui screens are
// converted to Bot API payloads.
package telegrambot

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/telegramui"
)

const (
	MaxMessageTextBytes  = 4096
	MaxCallbackTextBytes = 200
	MaxResponseBytes     = 2 << 20
	MaxRichTextRunes     = 32768
	MaxRichPanePNGBytes  = 10 << 20
	MaxTelegramFileBytes = 20 << 20
)

var (
	ErrNotPrivateDM = errors.New("telegram update is not an accepted private DM")
	// ErrUnsupportedMessageContent also covers protected forwards for which
	// Telegram omits the original body. There is no Bot API recovery call once
	// an update arrives without text, caption, quote, or supported media.
	ErrUnsupportedMessageContent = errors.New("telegram message has no supported content")
	ErrTelegramFileTooLarge      = errors.New("telegram file exceeds size limit")
)

type GetUpdatesRequest struct {
	Offset  int64
	Limit   int
	Timeout int
}

type MessageRequest struct {
	ChatID    int64
	Text      string
	ParseMode telegramui.ParseMode
	Grid      telegramui.Grid
}

type EditMessageRequest struct {
	ChatID    int64
	MessageID int64
	Text      string
	ParseMode telegramui.ParseMode
	Grid      telegramui.Grid
}

type Message struct {
	ChatID          int64
	MessageID       int64
	Text            string
	Rich            bool
	RichMediaFileID string
	PaneHash        string
}

type BotIdentity struct {
	ID       int64
	Username string
}

type IncomingKind string

const (
	IncomingMessage  IncomingKind = "message"
	IncomingCallback IncomingKind = "callback"
)

type IncomingContentKind string

const (
	IncomingText     IncomingContentKind = "text"
	IncomingVoice    IncomingContentKind = "voice"
	IncomingPhoto    IncomingContentKind = "photo"
	IncomingDocument IncomingContentKind = "document"
)

// ContentDescriptor deliberately carries no downloaded bytes. File data is
// fetched by the node that will consume it after routing has completed.
type ContentDescriptor struct {
	Kind         IncomingContentKind
	FileID       string
	FileUniqueID string
	FileName     string
	MIMEType     string
	FileSize     int64
	Width        int
	Height       int
	Duration     int
	Caption      string
	HiddenLinks  []string
}

type UserDescriptor struct {
	ID        int64
	IsBot     bool
	FirstName string
	LastName  string
	Username  string
	Link      string
}

type ChatDescriptor struct {
	ID       int64
	Type     string
	Title    string
	Username string
	Link     string
}

type ForwardOriginKind string

const (
	ForwardOriginUser       ForwardOriginKind = "user"
	ForwardOriginHiddenUser ForwardOriginKind = "hidden_user"
	ForwardOriginChat       ForwardOriginKind = "chat"
	ForwardOriginChannel    ForwardOriginKind = "channel"
)

type ForwardOrigin struct {
	Kind             ForwardOriginKind
	Date             int64
	Sender           *UserDescriptor
	HiddenSenderName string
	Chat             *ChatDescriptor
	MessageID        int64
	AuthorSignature  string
	Link             string
}

type TextLink struct {
	URL         string
	Text        string
	OffsetUTF16 int
	LengthUTF16 int
}

type QuoteDescriptor struct {
	Text     string
	Links    []TextLink
	Position int
	IsManual bool
}

type ExternalReplyDescriptor struct {
	Origin    *ForwardOrigin
	Chat      *ChatDescriptor
	MessageID int64
	Content   *ContentDescriptor
}

type RemoteFile struct {
	FileID       string
	FileUniqueID string
	FileSize     int64
	FilePath     string
}

// IncomingUpdate is the transport-safe subset accepted from Telegram. It is
// created only after private-DM checks pass.
type IncomingUpdate struct {
	UpdateID       int64
	Kind           IncomingKind
	MessageDate    int64
	ChatID         int64
	UserID         int64
	LanguageCode   string
	MessageID      int64
	Text           string
	Content        ContentDescriptor
	Links          []TextLink
	ForwardOrigin  *ForwardOrigin
	ViaBot         *UserDescriptor
	Quote          *QuoteDescriptor
	ExternalReply  *ExternalReplyDescriptor
	CallbackID     string
	CallbackData   string
	CallbackOrigin Message
}

// API is the small Bot API surface owned by this adapter.
type API interface {
	GetUpdates(context.Context, GetUpdatesRequest) ([]Update, error)
	AnswerCallbackQuery(context.Context, string, string) error
	SendMessage(context.Context, MessageRequest) (Message, error)
	EditMessage(context.Context, EditMessageRequest) (Message, error)
}

type UpdateHandler interface {
	HandleTelegramUpdate(context.Context, IncomingUpdate) error
}

type UpdateHandlerFunc func(context.Context, IncomingUpdate) error

func (f UpdateHandlerFunc) HandleTelegramUpdate(ctx context.Context, update IncomingUpdate) error {
	return f(ctx, update)
}

type Leadership interface {
	IsLeader() bool
}

// Cursor stores Telegram's next_update_id outside the poller process. A
// production implementation is expected to replicate it so leadership
// failover does not replay an already acknowledged update stream.
type Cursor interface {
	Load(context.Context) (int64, error)
	Commit(context.Context, int64) error
}

// CursorFuncs is a wiring adapter for a replicated cursor service without
// coupling this package to its application-layer type.
type CursorFuncs struct {
	LoadFunc   func(context.Context) (int64, error)
	CommitFunc func(context.Context, int64) error
}

func (c CursorFuncs) Load(ctx context.Context) (int64, error) {
	if c.LoadFunc == nil {
		return 0, errors.New("Telegram cursor load function is required")
	}
	return c.LoadFunc(ctx)
}

func (c CursorFuncs) Commit(ctx context.Context, next int64) error {
	if c.CommitFunc == nil {
		return errors.New("Telegram cursor commit function is required")
	}
	return c.CommitFunc(ctx, next)
}
