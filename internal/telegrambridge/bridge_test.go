package telegrambridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
)

func TestSourceBootstrapsByForgettingBacklogAndReturnsOnlyPositiveFence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want int64
	}{
		{
			name: "stale update",
			body: `{"ok":true,"result":[{"update_id":77,"message":{"message_id":1,"from":{"id":2},"chat":{"id":3,"type":"private"},"text":"old"}}]}`,
			want: 78,
		},
		{
			name: "empty backlog",
			body: `{"ok":true,"result":[]}`,
			want: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/bot123:test/bootstrap/getUpdates" {
					t.Fatalf("path = %q, want getUpdates", request.URL.Path)
				}
				var body telegram.GetUpdatesRequest
				decodeJSON(t, request, &body)
				if body.Offset != -1 || body.Limit != 1 || body.TimeoutSeconds != 0 {
					t.Fatalf("bootstrap request = %#v, want offset=-1 limit=1 timeout=0", body)
				}
				return response(http.StatusOK, test.body), nil
			})
			source, err := telegrambridge.NewSource(client)
			if err != nil {
				t.Fatalf("NewSource() error = %v", err)
			}

			got, err := source.Bootstrap(context.Background())
			if err != nil {
				t.Fatalf("Bootstrap() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Bootstrap() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSourceRetriesTransientBootstrapFailureBeforeReturningFence(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		var body telegram.GetUpdatesRequest
		decodeJSON(t, request, &body)
		if body.Offset != -1 || body.Limit != 1 || body.TimeoutSeconds != 0 {
			t.Fatalf("bootstrap attempt %d = %#v", calls, body)
		}
		if calls == 1 {
			return nil, errors.New("temporary connection failure")
		}
		return response(http.StatusOK, `{"ok":true,"result":[{"update_id":90,"message":{"message_id":1,"from":{"id":2},"chat":{"id":3,"type":"private"},"text":"old"}}]}`), nil
	})
	source, err := telegrambridge.NewSourceWithOptions(client, telegrambridge.RetryOptions{Delay: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := source.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if calls != 2 || fence != 91 {
		t.Fatalf("Bootstrap() calls/fence = %d/%d, want transient retry then fence 91", calls, fence)
	}
}

func TestSourcePollsExactOffsetAndNormalizesSupportedUpdates(t *testing.T) {
	t.Parallel()

	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		var body telegram.GetUpdatesRequest
		decodeJSON(t, request, &body)
		if body.Offset != 91 || body.Limit != 100 || body.TimeoutSeconds != 20 {
			t.Fatalf("poll request = %#v, want offset=91 limit=100 timeout=20", body)
		}
		if got := strings.Join(body.AllowedUpdates, ","); got != "message,callback_query" {
			t.Fatalf("allowed_updates = %q, want message,callback_query", got)
		}
		return response(http.StatusOK, `{"ok":true,"result":[
  {"update_id":91,"message":{"message_id":11,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/status"}},
  {"update_id":92,"callback_query":{"id":"opaque","from":{"id":43},"message":{"message_id":12,"from":{"id":600,"is_bot":true},"chat":{"id":44,"type":"group"}},"data":"page:next"}},
  {"update_id":93,"future_update":{"value":true}}
]}`), nil
	})
	source, err := telegrambridge.NewSource(client)
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}

	got, err := source.Poll(context.Background(), 91)
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	want := []coordinator.Update{
		{
			ID: 91, Kind: coordinator.UpdateMessage, ActorID: 42,
			ConversationID: 42, ConversationKind: "private", Text: "/status", SourceMessageID: 11,
		},
		{
			ID: 92, Kind: coordinator.UpdateCallback, ActorID: 43,
			ConversationID: 44, ConversationKind: "group", Text: "page:next",
			CallbackQueryID: "opaque", SourceMessageID: 12,
		},
		{ID: 93},
	}
	if len(got) != len(want) {
		t.Fatalf("Poll() returned %d updates, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("Poll()[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestSourceNormalizesReplyCaptionAndDownloadPolicyForTypedMedia(t *testing.T) {
	t.Parallel()

	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"ok":true,"result":[
  {"update_id":101,"message":{"message_id":201,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"voice note","reply_to_message":{"message_id":190},"voice":{"file_id":"voice-file","file_unique_id":"voice-unique","duration":7,"mime_type":"audio/ogg","file_size":4096}}},
  {"update_id":102,"message":{"message_id":202,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"photo note","photo":[{"file_id":"wide","file_unique_id":"wide-u","width":400,"height":100,"file_size":200},{"file_id":"large","file_unique_id":"large-u","width":300,"height":300,"file_size":9000}]}},
  {"update_id":103,"message":{"message_id":203,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"video caption only","video":{"file_id":"video-file","file_unique_id":"video-unique","width":1920,"height":1080,"duration":12,"mime_type":"video/mp4","file_size":2000000}}}
]}`), nil
	})
	source, err := telegrambridge.NewSource(client)
	if err != nil {
		t.Fatal(err)
	}

	got, err := source.Poll(context.Background(), 101)
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	want := []coordinator.Update{
		{
			ID: 101, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42,
			ConversationKind: "private", SourceMessageID: 201, ReplyToMessageID: 190,
			Caption: "voice note", MediaKind: "voice", MediaFileID: "voice-file",
			MediaFileUniqueID: "voice-unique", MediaFileSize: 4096, MediaMIMEType: "audio/ogg",
			MediaDurationSeconds: 7, MediaDownloadAllowed: true,
		},
		{
			ID: 102, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42,
			ConversationKind: "private", SourceMessageID: 202, Caption: "photo note",
			MediaKind: "photo", MediaFileID: "large", MediaFileUniqueID: "large-u",
			MediaFileSize: 9000, MediaWidth: 300, MediaHeight: 300, MediaDownloadAllowed: true,
		},
		{
			ID: 103, Kind: coordinator.UpdateMessage, ActorID: 42, ConversationID: 42,
			ConversationKind: "private", SourceMessageID: 203, Caption: "video caption only",
			MediaKind: "video", MediaFileID: "video-file", MediaFileUniqueID: "video-unique",
			MediaFileSize: 2000000, MediaMIMEType: "video/mp4", MediaDurationSeconds: 12,
			MediaWidth: 1920, MediaHeight: 1080, MediaDownloadAllowed: false,
		},
	}
	if len(got) != len(want) {
		t.Fatalf("Poll() updates = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("Poll()[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestSourceRejectsNonPositiveLiveOffsetWithoutHTTP(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusOK, `{"ok":true,"result":[]}`), nil
	})
	source, err := telegrambridge.NewSource(client)
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	if _, err := source.Poll(context.Background(), 0); err == nil {
		t.Fatal("Poll(0) error = nil")
	}
	if calls != 0 {
		t.Fatalf("Poll(0) made %d HTTP calls, want 0", calls)
	}
}

func TestSourceRetriesTransientPollFailureAtTheSameDurableOffset(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		calls++
		var body telegram.GetUpdatesRequest
		decodeJSON(t, request, &body)
		if body.Offset != 91 {
			t.Fatalf("poll attempt %d offset = %d, want unchanged 91", calls, body.Offset)
		}
		if calls == 1 {
			return nil, errors.New("temporary DNS failure with secret")
		}
		return response(http.StatusOK, `{"ok":true,"result":[{"update_id":91,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/status"}}]}`), nil
	})
	source, err := telegrambridge.NewSourceWithOptions(client, telegrambridge.RetryOptions{Delay: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewSourceWithOptions() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	updates, err := source.Poll(ctx, 91)
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if calls != 2 || len(updates) != 1 || updates[0].ID != 91 {
		t.Fatalf("Poll() calls/updates = %d/%#v, want two attempts then update 91", calls, updates)
	}
}

func TestSourceDoesNotRetryPermanentAPIOrProtocolFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "unauthorized", body: `{"ok":false,"error_code":401}`, code: http.StatusUnauthorized},
		{name: "forbidden", body: `{"ok":false,"error_code":403}`, code: http.StatusForbidden},
		{name: "concurrent poller", body: `{"ok":false,"error_code":409}`, code: http.StatusConflict},
		{name: "invalid protocol", body: `{"ok":`, code: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(test.code, test.body), nil
			})
			source, err := telegrambridge.NewSourceWithOptions(client, telegrambridge.RetryOptions{Delay: time.Nanosecond})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Poll(context.Background(), 91); err == nil {
				t.Fatal("Poll() error = nil")
			}
			if calls != 1 {
				t.Fatalf("Poll() calls = %d, want one fail-closed attempt", calls)
			}
		})
	}
}

func TestReadinessRetriesTransientGetMeButNotIdentityRejection(t *testing.T) {
	t.Parallel()

	t.Run("transient then ready", func(t *testing.T) {
		calls := 0
		client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("temporary connection failure")
			}
			return response(http.StatusOK, `{"ok":true,"result":{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}}`), nil
		})
		readiness, err := telegrambridge.NewPersistentReadinessWithOptions(
			client,
			"my_bria_bot",
			telegrambridge.RetryOptions{Delay: time.Nanosecond},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := readiness.Ready(context.Background(), coordinator.Checkpoint{}); err != nil {
			t.Fatalf("Ready() error = %v", err)
		}
		if calls != 2 {
			t.Fatalf("getMe calls = %d, want retry then success", calls)
		}
	})

	t.Run("authentication rejected", func(t *testing.T) {
		calls := 0
		client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusUnauthorized, `{"ok":false,"error_code":401}`), nil
		})
		readiness, err := telegrambridge.NewPersistentReadinessWithOptions(
			client,
			"my_bria_bot",
			telegrambridge.RetryOptions{Delay: time.Nanosecond},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := readiness.Ready(context.Background(), coordinator.Checkpoint{}); err == nil {
			t.Fatal("Ready() error = nil")
		}
		if calls != 1 {
			t.Fatalf("getMe calls = %d, want one fail-closed attempt", calls)
		}
	})
}

func TestOneShotReadinessReturnsTransientFailureWithoutRetry(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("temporary connection failure")
	})
	readiness, err := telegrambridge.NewReadiness(client, "my_bria_bot")
	if err != nil {
		t.Fatal(err)
	}
	if err := readiness.Ready(context.Background(), coordinator.Checkpoint{}); err == nil {
		t.Fatal("one-shot Ready() error = nil")
	}
	if calls != 1 {
		t.Fatalf("one-shot getMe calls = %d, want 1", calls)
	}
}

func TestRetryOptionsRejectNegativeDelay(t *testing.T) {
	t.Parallel()

	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"ok":true,"result":[]}`), nil
	})
	if _, err := telegrambridge.NewSourceWithOptions(client, telegrambridge.RetryOptions{Delay: -1}); err == nil {
		t.Fatal("NewSourceWithOptions(negative delay) error = nil")
	}
	if _, err := telegrambridge.NewSourceWithOptions(client, telegrambridge.RetryOptions{Delay: 31 * time.Second}); err == nil {
		t.Fatal("NewSourceWithOptions(overlong delay) error = nil")
	}
	if _, err := telegrambridge.NewPersistentReadinessWithOptions(client, "my_bria_bot", telegrambridge.RetryOptions{Delay: -1}); err == nil {
		t.Fatal("NewPersistentReadinessWithOptions(negative delay) error = nil")
	}
}

func TestSenderReturnsOnlyPositiveTelegramReceipt(t *testing.T) {
	t.Parallel()

	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bot123:test/bootstrap/sendRichMessage" {
			t.Fatalf("path = %q, want sendRichMessage", request.URL.Path)
		}
		var body telegram.SendRichMessageRequest
		decodeJSON(t, request, &body)
		if body.ChatID != 42 || body.RichMessage.Markdown != "Bria ready" || body.ReplyMarkup != nil {
			t.Fatalf("send body = %#v", body)
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":501,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"Bria ready"}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}

	receipt, err := sender.SendStatus(context.Background(), "status:91", coordinator.Status{
		ConversationID: 42,
		Text:           "Bria ready",
	})
	if err != nil {
		t.Fatalf("SendStatus() error = %v", err)
	}
	if receipt != (coordinator.Receipt{MessageID: 501}) {
		t.Fatalf("SendStatus() = %#v, want message id 501 only", receipt)
	}
}

func TestSenderDeactivatesOnlyTheExactPreviousCarrierKeyboard(t *testing.T) {
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bot123:test/bootstrap/editMessageReplyMarkup" {
			t.Fatalf("path=%q", request.URL.Path)
		}
		var body telegram.EditMessageReplyMarkupRequest
		decodeJSON(t, request, &body)
		if body.ChatID != 42 || body.MessageID != 501 || body.ReplyMarkup.InlineKeyboard == nil || len(body.ReplyMarkup.InlineKeyboard) != 0 {
			t.Fatalf("body=%#v", body)
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":501,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"preserved"}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.DeactivateInlineKeyboard(context.Background(), "retire:501", 42, 501); err != nil {
		t.Fatal(err)
	}
}

func TestSenderTreatsAlreadyDeactivatedKeyboardAsSuccess(t *testing.T) {
	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.DeactivateInlineKeyboard(context.Background(), "retire:501", 42, 501); err != nil {
		t.Fatalf("idempotent keyboard retirement: %v", err)
	}
}

func TestSenderNormalizesProviderCodeForRichRendering(t *testing.T) {
	t.Parallel()

	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		var body telegram.SendRichMessageRequest
		decodeJSON(t, request, &body)
		if body.RichMessage.Markdown != "**Проверка**\n<pre><code class=\"language-text\">/root</code></pre>" {
			t.Fatalf("formatted Markdown = %q", body.RichMessage.Markdown)
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":502,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"formatted"}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.SendStatus(context.Background(), "status:markdown", coordinator.Status{
		ConversationID: 42,
		Text:           "**Проверка**\n```text\n/root\n```",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSenderUsesNativeRichMessageForStatusTables(t *testing.T) {
	t.Parallel()

	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bot123:test/bootstrap/sendRichMessage" {
			t.Fatalf("path = %q, want sendRichMessage", request.URL.Path)
		}
		var body telegram.SendRichMessageRequest
		decodeJSON(t, request, &body)
		if body.ChatID != 42 || !strings.Contains(body.RichMessage.Markdown, "| <sub>Сервер</sub> |") || body.ReplyMarkup == nil {
			t.Fatalf("rich send body = %#v", body)
		}
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":503,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"}}}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	keyboard := coordinator.KeyboardMarkup{{{Text: "≡ Меню", CallbackData: "signed"}}}
	if _, err := sender.SendStatusWithKeyboard(context.Background(), "status:rich", coordinator.Status{
		ConversationID: 42, RichMarkdown: true,
		Text: "Статус\n\n\u00a0\n\n| Сервер | Бэк |\n|---|---|\n| local | codex |",
	}, &keyboard); err != nil {
		t.Fatal(err)
	}
}

func TestSenderNeverRetriesTransientSendFailure(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("ambiguous send result")
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.SendStatus(context.Background(), "status:91", coordinator.Status{
		ConversationID: 42,
		Text:           "Bria ready",
	}); err == nil {
		t.Fatal("SendStatus() error = nil")
	}
	if calls != 1 {
		t.Fatalf("sendMessage calls = %d, want one ambiguous attempt without retry", calls)
	}
}

func TestSenderCallbackAcknowledgementFailureDoesNotBlockCardEdit(t *testing.T) {
	t.Parallel()

	ackRelease := make(chan struct{})
	editReached := make(chan struct{})
	ackCompleted := make(chan telegrambridge.CallbackAcknowledgementState, 1)
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/answerCallbackQuery"):
			<-ackRelease
			return response(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"query is too old"}`), nil
		case strings.HasSuffix(request.URL.Path, "/editMessageText"):
			close(editReached)
			return response(http.StatusOK, `{"ok":true,"result":{"message_id":77,"from":{"id":600,"is_bot":true},"chat":{"id":42,"type":"private"},"text":"Page 2"}}`), nil
		default:
			t.Fatalf("unexpected Telegram method %q", request.URL.Path)
			return nil, nil
		}
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.BindCallbackAcknowledgements(callbackAcknowledgementRecorder{
		begin: func(operationID, callbackQueryID string) (bool, error) {
			if operationID != "status:92" || callbackQueryID != "callback-1" {
				t.Fatalf("acknowledgement identity = %q/%q", operationID, callbackQueryID)
			}
			return true, nil
		},
		complete: func(_ string, _ string, state telegrambridge.CallbackAcknowledgementState) error {
			ackCompleted <- state
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	receipt, err := sender.EditStatusWithKeyboard(context.Background(), "status:92", coordinator.Status{
		ConversationID:  42,
		Text:            "Page 2",
		CallbackQueryID: "callback-1",
		SourceMessageID: 77,
	}, nil)
	if err != nil {
		t.Fatalf("EditStatusWithKeyboard() error = %v", err)
	}
	select {
	case <-editReached:
	default:
		t.Fatal("visible card edit did not complete independently")
	}
	if receipt.MessageID != 77 {
		t.Fatalf("receipt = %#v, want successful card edit", receipt)
	}
	select {
	case state := <-ackCompleted:
		t.Fatalf("acknowledgement completed before release with state %q", state)
	default:
	}
	close(ackRelease)
	select {
	case state := <-ackCompleted:
		if state != telegrambridge.CallbackAcknowledgementFailed {
			t.Fatalf("acknowledgement state = %q, want failed", state)
		}
	case <-time.After(time.Second):
		t.Fatal("callback acknowledgement outcome was not persisted")
	}
}

func TestSenderDirectCallbackAcknowledgementDoesNotMutateTelegramMessages(t *testing.T) {
	t.Parallel()

	acknowledged := make(chan struct{}, 1)
	client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/answerCallbackQuery") {
			t.Fatalf("unexpected visible Telegram mutation %q", request.URL.Path)
		}
		acknowledged <- struct{}{}
		return response(http.StatusOK, `{"ok":true,"result":true}`), nil
	})
	sender, err := telegrambridge.NewSender(client)
	if err != nil {
		t.Fatal(err)
	}
	sender.AcknowledgeCallback(context.Background(), "status:93", "callback-2")
	select {
	case <-acknowledged:
	case <-time.After(time.Second):
		t.Fatal("callback was not acknowledged")
	}
}

type callbackAcknowledgementRecorder struct {
	begin    func(string, string) (bool, error)
	complete func(string, string, telegrambridge.CallbackAcknowledgementState) error
}

func (recorder callbackAcknowledgementRecorder) BeginCallbackAcknowledgement(_ context.Context, operationID, callbackQueryID string) (bool, error) {
	return recorder.begin(operationID, callbackQueryID)
}

func (recorder callbackAcknowledgementRecorder) CompleteCallbackAcknowledgement(_ context.Context, operationID, callbackQueryID string, state telegrambridge.CallbackAcknowledgementState) error {
	return recorder.complete(operationID, callbackQueryID, state)
}

func TestSourceTransientRetryWaitHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	calls := 0
	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("temporary connection failure")
	})
	source, err := telegrambridge.NewSourceWithOptions(client, telegrambridge.RetryOptions{Delay: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := source.Poll(ctx, 91); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Poll() error = %v, want caller deadline", err)
	}
	if calls != 1 {
		t.Fatalf("poll attempts = %d, want no retry after context deadline", calls)
	}
}

func TestReadinessRequiresExactExpectedBotIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity string
		wantErr  bool
	}{
		{name: "exact", identity: `{"id":600,"is_bot":true,"first_name":"Bria","username":"my_bria_bot"}`},
		{name: "different username", identity: `{"id":600,"is_bot":true,"first_name":"Bria","username":"other_bot"}`, wantErr: true},
		{name: "case differs", identity: `{"id":600,"is_bot":true,"first_name":"Bria","username":"My_Bria_Bot"}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := mustTelegramClient(t, func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/bot123:test/bootstrap/getMe" {
					t.Fatalf("path = %q, want getMe", request.URL.Path)
				}
				return response(http.StatusOK, `{"ok":true,"result":`+test.identity+`}`), nil
			})
			readiness, err := telegrambridge.NewReadiness(client, "my_bria_bot")
			if err != nil {
				t.Fatalf("NewReadiness() error = %v", err)
			}
			err = readiness.Ready(context.Background(), coordinator.Checkpoint{})
			if (err != nil) != test.wantErr {
				t.Fatalf("Ready() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestBridgeConstructorsRejectMissingDependenciesAndInvalidExpectedUsername(t *testing.T) {
	t.Parallel()

	if _, err := telegrambridge.NewSource(nil); err == nil {
		t.Fatal("NewSource(nil) error = nil")
	}
	if _, err := telegrambridge.NewSender(nil); err == nil {
		t.Fatal("NewSender(nil) error = nil")
	}
	client := mustTelegramClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
	})
	for _, username := range []string{"", "@my_bria_bot", "my bria bot", " my_bria_bot"} {
		if _, err := telegrambridge.NewReadiness(client, username); err == nil {
			t.Errorf("NewReadiness(%q) error = nil", username)
		}
	}
	if _, err := telegrambridge.NewReadiness(nil, "my_bria_bot"); err == nil {
		t.Fatal("NewReadiness(nil) error = nil")
	}
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (function httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func mustTelegramClient(t *testing.T, do httpClientFunc) *telegram.Client {
	t.Helper()
	client, err := telegram.NewClient("123:test/bootstrap", do, telegram.Options{})
	if err != nil {
		t.Fatalf("telegram.NewClient() error = %v", err)
	}
	return client
}

func decodeJSON(t *testing.T, request *http.Request, result any) {
	t.Helper()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
