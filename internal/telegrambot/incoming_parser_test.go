package telegrambot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParsePrivateDMForwardedTextPreservesContext(t *testing.T) {
	update := parseRawPrivateDM(t, `{
  "update_id": 91,
  "message": {
    "message_id": 7, "date": 1786975200,
    "from": {"id": 42, "is_bot": false, "language_code": "ru"},
    "chat": {"id": 42, "type": "private"},
    "text": "😀 docs",
    "entities": [{"type": "text_link", "offset": 3, "length": 4, "url": "https://example.test/docs"}],
    "forward_origin": {
      "type": "user", "date": 123,
      "sender_user": {"id": 77, "is_bot": true, "first_name": "Helper", "username": "helperbot"}
    },
    "via_bot": {"id": 88, "is_bot": true, "first_name": "Relay", "username": "relay_bot"},
    "quote": {
      "text": "quoted link", "position": 2, "is_manual": true,
      "entities": [{"type": "text_link", "offset": 7, "length": 4, "url": "https://quote.test"}]
    }
  }
}`)

	wantText := "[forwarded from @helperbot]\n😀 docs\nLinks:\nhttps://example.test/docs"
	if update.Text != wantText || update.Content.Kind != IncomingText ||
		len(update.Content.HiddenLinks) != 1 || update.Content.HiddenLinks[0] != "https://example.test/docs" {
		t.Fatalf("content = %#v, text = %q", update.Content, update.Text)
	}
	if update.MessageDate != 1786975200 {
		t.Fatalf("message date = %d", update.MessageDate)
	}
	if len(update.Links) != 1 || update.Links[0].Text != "docs" ||
		update.Links[0].OffsetUTF16 != 3 {
		t.Fatalf("links = %#v", update.Links)
	}
	if update.ForwardOrigin == nil || update.ForwardOrigin.Sender == nil ||
		!update.ForwardOrigin.Sender.IsBot || update.ForwardOrigin.Link != "https://t.me/helperbot" {
		t.Fatalf("origin = %#v", update.ForwardOrigin)
	}
	if update.ViaBot == nil || !update.ViaBot.IsBot || update.ViaBot.Link != "https://t.me/relay_bot" {
		t.Fatalf("via bot = %#v", update.ViaBot)
	}
	if update.Quote == nil || !update.Quote.IsManual || update.Quote.Position != 2 ||
		len(update.Quote.Links) != 1 || update.Quote.Links[0].Text != "link" {
		t.Fatalf("quote = %#v", update.Quote)
	}
}

func TestParsePrivateDMTextSentViaAnotherBotPreservesAttribution(t *testing.T) {
	update := parseRawPrivateDM(t, `{
  "update_id":1,"message":{
    "message_id":2,"from":{"id":42,"is_bot":false},"chat":{"id":42,"type":"private"},
    "text":"generated alert","via_bot":{"id":88,"is_bot":true,"username":"relay_bot"}
  }
}`)
	if update.Text != "[via @relay_bot]\ngenerated alert" || update.ViaBot == nil ||
		!update.ViaBot.IsBot {
		t.Fatalf("via-bot update=%#v", update)
	}
}

func TestParsePrivateDMMediaDescriptorsAndCaptionSalvage(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind IncomingContentKind
		id   string
		text string
	}{
		{
			name: "voice",
			raw:  `"voice":{"file_id":"voice-id","file_unique_id":"voice-unique","duration":9,"mime_type":"audio/ogg","file_size":321}`,
			kind: IncomingVoice, id: "voice-id",
		},
		{
			name: "largest photo",
			raw: `"photo":[
          {"file_id":"medium","file_unique_id":"m","width":400,"height":300,"file_size":500},
          {"file_id":"largest","file_unique_id":"l","width":1200,"height":800,"file_size":900},
          {"file_id":"small","file_unique_id":"s","width":90,"height":90,"file_size":100}
        ], "caption":"inspect docs", "caption_entities":[
          {"type":"text_link","offset":8,"length":4,"url":"https://photo.test"}
        ]`,
			kind: IncomingPhoto, id: "largest",
			text: "inspect docs\nLinks:\nhttps://photo.test",
		},
		{
			name: "document",
			raw:  `"document":{"file_id":"doc-id","file_unique_id":"doc-unique","file_name":"report.pdf","mime_type":"application/pdf","file_size":654}, "caption":"review"`,
			kind: IncomingDocument, id: "doc-id", text: "review",
		},
		{
			name: "unsupported media caption is salvaged",
			raw:  `"video":{"file_id":"ignored"}, "caption":"keep this caption"`,
			kind: IncomingText, text: "keep this caption",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"update_id":1,"message":{"message_id":2,` +
				`"from":{"id":42,"is_bot":false},"chat":{"id":42,"type":"private"},` +
				test.raw + `}}`
			update := parseRawPrivateDM(t, raw)
			if update.Content.Kind != test.kind || update.Content.FileID != test.id {
				t.Fatalf("descriptor = %#v", update.Content)
			}
			if test.text != "" && update.Text != test.text {
				t.Fatalf("text = %q, want %q", update.Text, test.text)
			}
			if test.kind == IncomingDocument &&
				(update.Content.FileName != "report.pdf" || update.Content.MIMEType != "application/pdf" ||
					update.Content.Caption != "review") {
				t.Fatalf("document = %#v", update.Content)
			}
		})
	}
}

func TestParsePrivateDMAllForwardOriginsAndExternalReply(t *testing.T) {
	tests := []struct {
		name       string
		originJSON string
		prefix     string
		link       string
	}{
		{
			name:       "hidden user",
			originJSON: `{"type":"hidden_user","date":1,"sender_user_name":"Anonymous"}`,
			prefix:     "[forwarded from Anonymous]", link: "",
		},
		{
			name:       "chat",
			originJSON: `{"type":"chat","date":1,"sender_chat":{"id":-1,"type":"group","title":"Team","username":"team_chat"}}`,
			prefix:     "[forwarded from @team_chat]", link: "https://t.me/team_chat",
		},
		{
			name:       "channel",
			originJSON: `{"type":"channel","date":1,"chat":{"id":-2,"type":"channel","title":"News","username":"news"},"message_id":17,"author_signature":"Ed"}`,
			prefix:     "[forwarded from @news]", link: "https://t.me/news/17",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"update_id":1,"message":{"message_id":2,` +
				`"from":{"id":42,"is_bot":false},"chat":{"id":42,"type":"private"},` +
				`"text":"body","forward_origin":` + test.originJSON + `}}`
			update := parseRawPrivateDM(t, raw)
			if !strings.HasPrefix(update.Text, test.prefix) || update.ForwardOrigin == nil ||
				update.ForwardOrigin.Link != test.link {
				t.Fatalf("update = %#v", update)
			}
		})
	}

	update := parseRawPrivateDM(t, `{
  "update_id":1,"message":{
    "message_id":2,"from":{"id":42,"is_bot":false},"chat":{"id":42,"type":"private"},"text":"reply",
    "external_reply":{
      "origin":{"type":"channel","date":1,"chat":{"id":-3,"type":"channel","username":"source"},"message_id":9},
      "chat":{"id":-3,"type":"channel","title":"Source","username":"source"},"message_id":9,
      "document":{"file_id":"external-doc","file_unique_id":"external-unique","file_name":"x.txt","mime_type":"text/plain","file_size":12}
    }
  }
}`)
	if update.ExternalReply == nil || update.ExternalReply.Content == nil ||
		update.ExternalReply.Content.FileID != "external-doc" ||
		update.ExternalReply.Origin == nil || update.ExternalReply.Origin.Link != "https://t.me/source/9" {
		t.Fatalf("external reply = %#v", update.ExternalReply)
	}
}

func TestParsePrivateDMProtectedForwardWithoutRecoverableContent(t *testing.T) {
	raw := `{"update_id":1,"message":{"message_id":2,"from":{"id":42,"is_bot":false},"chat":{"id":42,"type":"private"},"forward_origin":{"type":"hidden_user","date":1,"sender_user_name":"Hidden"}}}`
	var api apiUpdate
	if err := json.Unmarshal([]byte(raw), &api); err != nil {
		t.Fatal(err)
	}
	_, err := ParsePrivateDM(fromAPIUpdate(api))
	if !errors.Is(err, ErrUnsupportedMessageContent) {
		t.Fatalf("error = %v", err)
	}
}

func TestParsePrivateDMSalvagesQuoteWhenForwardBodyIsUnavailable(t *testing.T) {
	update := parseRawPrivateDM(t, `{
  "update_id":1,"message":{
    "message_id":2,"from":{"id":42,"is_bot":false},"chat":{"id":42,"type":"private"},
    "forward_origin":{"type":"hidden_user","date":1,"sender_user_name":"Hidden bot"},
    "quote":{"text":"recoverable alert text","position":0,"is_manual":true}
  }
}`)
	if update.Text != "[forwarded from Hidden bot]\nrecoverable alert text" ||
		update.Content.Kind != IncomingText {
		t.Fatalf("salvaged update=%#v", update)
	}
}

func parseRawPrivateDM(t *testing.T, raw string) IncomingUpdate {
	t.Helper()
	var api apiUpdate
	if err := json.Unmarshal([]byte(raw), &api); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	update, err := ParsePrivateDM(fromAPIUpdate(api))
	if err != nil {
		t.Fatalf("ParsePrivateDM: %v", err)
	}
	return update
}
