package telegrambot

import "encoding/json"

type apiEnvelope[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	ErrorCode   int    `json:"error_code,omitempty"`
	Description string `json:"description,omitempty"`
}

type apiUpdate struct {
	UpdateID      int64             `json:"update_id"`
	Message       *apiMessage       `json:"message,omitempty"`
	CallbackQuery *apiCallbackQuery `json:"callback_query,omitempty"`
}

type apiMessage struct {
	MessageID       int64                 `json:"message_id"`
	Date            int64                 `json:"date,omitempty"`
	From            *apiUser              `json:"from,omitempty"`
	Chat            apiChat               `json:"chat"`
	Text            string                `json:"text,omitempty"`
	Entities        []apiMessageEntity    `json:"entities,omitempty"`
	Caption         string                `json:"caption,omitempty"`
	CaptionEntities []apiMessageEntity    `json:"caption_entities,omitempty"`
	Voice           *apiVoice             `json:"voice,omitempty"`
	Photo           []apiPhotoSize        `json:"photo,omitempty"`
	Document        *apiDocument          `json:"document,omitempty"`
	ForwardOrigin   *apiMessageOrigin     `json:"forward_origin,omitempty"`
	ViaBot          *apiUser              `json:"via_bot,omitempty"`
	Quote           *apiTextQuote         `json:"quote,omitempty"`
	ExternalReply   *apiExternalReplyInfo `json:"external_reply,omitempty"`
	RichMessage     json.RawMessage       `json:"rich_message,omitempty"`
}

type apiUser struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name,omitempty"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

type apiChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

type apiMessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
}

type apiVoice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type apiPhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type apiDocument struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type apiMessageOrigin struct {
	Type            string   `json:"type"`
	Date            int64    `json:"date"`
	SenderUser      *apiUser `json:"sender_user,omitempty"`
	SenderUserName  string   `json:"sender_user_name,omitempty"`
	SenderChat      *apiChat `json:"sender_chat,omitempty"`
	Chat            *apiChat `json:"chat,omitempty"`
	MessageID       int64    `json:"message_id,omitempty"`
	AuthorSignature string   `json:"author_signature,omitempty"`
}

type apiTextQuote struct {
	Text     string             `json:"text"`
	Entities []apiMessageEntity `json:"entities,omitempty"`
	Position int                `json:"position"`
	IsManual bool               `json:"is_manual,omitempty"`
}

type apiExternalReplyInfo struct {
	Origin    *apiMessageOrigin `json:"origin,omitempty"`
	Chat      *apiChat          `json:"chat,omitempty"`
	MessageID int64             `json:"message_id,omitempty"`
	Voice     *apiVoice         `json:"voice,omitempty"`
	Photo     []apiPhotoSize    `json:"photo,omitempty"`
	Document  *apiDocument      `json:"document,omitempty"`
}

type apiCallbackQuery struct {
	ID      string      `json:"id"`
	From    apiUser     `json:"from"`
	Message *apiMessage `json:"message,omitempty"`
	Data    string      `json:"data,omitempty"`
}

type apiMessageResult struct {
	MessageID int64   `json:"message_id"`
	Chat      apiChat `json:"chat"`
}

type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type getUpdatesPayload struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type answerCallbackPayload struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
}

type sendChatActionPayload struct {
	ChatID int64  `json:"chat_id"`
	Action string `json:"action"`
}

type sendMessagePayload struct {
	ChatID      int64                `json:"chat_id"`
	Text        string               `json:"text"`
	ParseMode   string               `json:"parse_mode,omitempty"`
	LinkPreview linkPreviewOptions   `json:"link_preview_options"`
	ReplyMarkup inlineKeyboardMarkup `json:"reply_markup"`
}

type editMessagePayload struct {
	ChatID      int64                `json:"chat_id"`
	MessageID   int64                `json:"message_id"`
	Text        string               `json:"text"`
	ParseMode   string               `json:"parse_mode,omitempty"`
	LinkPreview linkPreviewOptions   `json:"link_preview_options"`
	ReplyMarkup inlineKeyboardMarkup `json:"reply_markup"`
}

type deleteMessagePayload struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

type editReplyMarkupPayload struct {
	ChatID      int64                `json:"chat_id"`
	MessageID   int64                `json:"message_id"`
	ReplyMarkup inlineKeyboardMarkup `json:"reply_markup"`
}

type getFilePayload struct {
	FileID string `json:"file_id"`
}

type apiFile struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

type richMessage struct {
	Markdown string          `json:"markdown"`
	Media    []richMediaItem `json:"media,omitempty"`
}

type richMediaItem struct {
	ID    string          `json:"id"`
	Media inputMediaPhoto `json:"media"`
}

type inputMediaPhoto struct {
	Type  string `json:"type"`
	Media string `json:"media"`
}

type sendRichMessagePayload struct {
	ChatID              int64                `json:"chat_id"`
	RichMessage         richMessage          `json:"rich_message"`
	DisableNotification bool                 `json:"disable_notification"`
	ReplyMarkup         inlineKeyboardMarkup `json:"reply_markup"`
}

type editRichMessagePayload struct {
	ChatID      int64                `json:"chat_id"`
	MessageID   int64                `json:"message_id"`
	RichMessage richMessage          `json:"rich_message"`
	ReplyMarkup inlineKeyboardMarkup `json:"reply_markup"`
}

type linkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

type Update struct {
	UpdateID      int64
	Message       *UpdateMessage
	CallbackQuery *CallbackQuery
}

type UpdateMessage struct {
	MessageID       int64
	Date            int64
	FromID          int64
	LanguageCode    string
	ChatID          int64
	ChatType        string
	Text            string
	Content         ContentDescriptor
	Links           []TextLink
	ForwardOrigin   *ForwardOrigin
	ViaBot          *UserDescriptor
	Quote           *QuoteDescriptor
	ExternalReply   *ExternalReplyDescriptor
	RichMediaFileID string
	RichMessage     bool
}

type CallbackQuery struct {
	ID               string
	FromID           int64
	FromLanguageCode string
	Message          *UpdateMessage
	Data             string
}

func fromAPIUpdate(raw apiUpdate) Update {
	return Update{
		UpdateID:      raw.UpdateID,
		Message:       fromAPIMessage(raw.Message),
		CallbackQuery: fromAPICallback(raw.CallbackQuery),
	}
}

func fromAPIMessage(raw *apiMessage) *UpdateMessage {
	if raw == nil {
		return nil
	}
	var fromID int64
	var languageCode string
	if raw.From != nil {
		fromID = raw.From.ID
		languageCode = raw.From.LanguageCode
	}
	content, text, links := contentFromAPIMessage(raw)
	return &UpdateMessage{
		MessageID:       raw.MessageID,
		Date:            raw.Date,
		FromID:          fromID,
		LanguageCode:    languageCode,
		ChatID:          raw.Chat.ID,
		ChatType:        raw.Chat.Type,
		Text:            text,
		Content:         content,
		Links:           links,
		ForwardOrigin:   originFromAPI(raw.ForwardOrigin),
		ViaBot:          userFromAPI(raw.ViaBot),
		Quote:           quoteFromAPI(raw.Quote),
		ExternalReply:   externalReplyFromAPI(raw.ExternalReply),
		RichMediaFileID: extractRichPhotoFileID(raw.RichMessage),
		RichMessage:     len(raw.RichMessage) > 0 && string(raw.RichMessage) != "null",
	}
}

func fromAPICallback(raw *apiCallbackQuery) *CallbackQuery {
	if raw == nil {
		return nil
	}
	return &CallbackQuery{
		ID: raw.ID, FromID: raw.From.ID, FromLanguageCode: raw.From.LanguageCode,
		Message: fromAPIMessage(raw.Message), Data: raw.Data,
	}
}
