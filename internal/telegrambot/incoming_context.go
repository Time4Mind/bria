package telegrambot

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

func originFromAPI(raw *apiMessageOrigin) *ForwardOrigin {
	if raw == nil {
		return nil
	}
	origin := &ForwardOrigin{
		Kind: ForwardOriginKind(raw.Type), Date: raw.Date, MessageID: raw.MessageID,
		AuthorSignature: raw.AuthorSignature, HiddenSenderName: raw.SenderUserName,
	}
	switch origin.Kind {
	case ForwardOriginUser:
		origin.Sender = userFromAPI(raw.SenderUser)
	case ForwardOriginHiddenUser:
	case ForwardOriginChat:
		origin.Chat = chatFromAPI(raw.SenderChat)
	case ForwardOriginChannel:
		origin.Chat = chatFromAPI(raw.Chat)
	}
	origin.Link = originLink(origin)
	return origin
}

func userFromAPI(raw *apiUser) *UserDescriptor {
	if raw == nil {
		return nil
	}
	return &UserDescriptor{
		ID: raw.ID, IsBot: raw.IsBot, FirstName: raw.FirstName, LastName: raw.LastName,
		Username: raw.Username, Link: usernameLink(raw.Username),
	}
}

func chatFromAPI(raw *apiChat) *ChatDescriptor {
	if raw == nil {
		return nil
	}
	return &ChatDescriptor{
		ID: raw.ID, Type: raw.Type, Title: raw.Title, Username: raw.Username,
		Link: usernameLink(raw.Username),
	}
}

func quoteFromAPI(raw *apiTextQuote) *QuoteDescriptor {
	if raw == nil {
		return nil
	}
	return &QuoteDescriptor{
		Text: raw.Text, Links: textLinks(raw.Text, raw.Entities),
		Position: raw.Position, IsManual: raw.IsManual,
	}
}

func externalReplyFromAPI(raw *apiExternalReplyInfo) *ExternalReplyDescriptor {
	if raw == nil {
		return nil
	}
	reply := &ExternalReplyDescriptor{
		Origin: originFromAPI(raw.Origin), Chat: chatFromAPI(raw.Chat), MessageID: raw.MessageID,
	}
	content := externalReplyContent(raw)
	if content.Kind != "" {
		reply.Content = &content
	}
	return reply
}

func textLinks(text string, entities []apiMessageEntity) []TextLink {
	links := make([]TextLink, 0)
	for _, entity := range entities {
		if entity.Type != "text_link" || strings.TrimSpace(entity.URL) == "" ||
			entity.Offset < 0 || entity.Length <= 0 {
			continue
		}
		links = append(links, TextLink{
			URL: entity.URL, Text: utf16Slice(text, entity.Offset, entity.Length),
			OffsetUTF16: entity.Offset, LengthUTF16: entity.Length,
		})
	}
	return links
}

func utf16Slice(text string, offset, length int) string {
	encoded := utf16.Encode([]rune(text))
	if offset < 0 || length <= 0 || offset > len(encoded) || length > len(encoded)-offset {
		return ""
	}
	return string(utf16.Decode(encoded[offset : offset+length]))
}

func hiddenLinkURLs(links []TextLink) []string {
	result := make([]string, 0, len(links))
	seen := make(map[string]struct{}, len(links))
	for _, link := range links {
		if _, exists := seen[link.URL]; exists {
			continue
		}
		seen[link.URL] = struct{}{}
		result = append(result, link.URL)
	}
	return result
}

func decorateIncomingText(
	text string,
	origin *ForwardOrigin,
	viaBot *UserDescriptor,
	links []TextLink,
) string {
	parts := make([]string, 0, 3)
	if prefix := forwardPrefix(origin); prefix != "" {
		parts = append(parts, prefix+text)
	} else if viaBot != nil && viaBot.IsBot {
		via := userDisplay(viaBot)
		parts = append(parts, fmt.Sprintf("[via %s]\n%s", via, text))
	} else if text != "" {
		parts = append(parts, text)
	}
	urls := hiddenLinkURLs(links)
	if len(urls) > 0 {
		parts = append(parts, "Links:", strings.Join(urls, "\n"))
	}
	return strings.Join(parts, "\n")
}

func forwardPrefix(origin *ForwardOrigin) string {
	if origin == nil {
		return ""
	}
	name := ""
	switch origin.Kind {
	case ForwardOriginUser:
		name = userDisplay(origin.Sender)
	case ForwardOriginHiddenUser:
		name = origin.HiddenSenderName
	case ForwardOriginChat, ForwardOriginChannel:
		name = chatDisplay(origin.Chat)
	}
	if name == "" {
		return "[forwarded]\n"
	}
	return fmt.Sprintf("[forwarded from %s]\n", name)
}

func userDisplay(user *UserDescriptor) string {
	if user == nil {
		return "@user"
	}
	if user.Username != "" {
		return "@" + user.Username
	}
	if user.FirstName != "" {
		return "@" + user.FirstName
	}
	return "@user"
}

func chatDisplay(chat *ChatDescriptor) string {
	if chat == nil {
		return "@channel"
	}
	if chat.Username != "" {
		return "@" + chat.Username
	}
	if chat.Title != "" {
		return "@" + chat.Title
	}
	return "@channel"
}

func usernameLink(username string) string {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return ""
	}
	return "https://t.me/" + username
}

func originLink(origin *ForwardOrigin) string {
	if origin == nil || origin.Chat == nil || origin.Chat.Username == "" {
		if origin != nil && origin.Sender != nil {
			return origin.Sender.Link
		}
		return ""
	}
	if origin.Kind == ForwardOriginChannel && origin.MessageID > 0 {
		return fmt.Sprintf("%s/%d", origin.Chat.Link, origin.MessageID)
	}
	return origin.Chat.Link
}
