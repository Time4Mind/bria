package interactioncomposition

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"bria/internal/telegram"
)

func TestTelegramDeleterTreatsOnlyDefinitiveExactAlreadyAbsentAsSuccess(t *testing.T) {
	t.Parallel()
	client, err := telegram.NewClient("123:test-only", httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/bot123:test-only/deleteMessage" {
			t.Fatalf("request path = %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"ok":false,"error_code":400,"description":"Bad Request: message to delete not found"}`,
			)),
		}, nil
	}), telegram.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := (telegramDeleter{client: client}).DeleteMessage(context.Background(), 42, 103); err != nil {
		t.Fatalf("real Client already-absent envelope error = %v", err)
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deleted", want: true},
		{name: "already absent", err: &telegram.APIError{Method: "deleteMessage", HTTPStatus: 400, ErrorCode: 400, Description: "Bad Request: message to delete not found"}, want: true},
		{name: "wrong method", err: &telegram.APIError{Method: "sendMessage", HTTPStatus: 400, ErrorCode: 400, Description: "Bad Request: message to delete not found"}},
		{name: "permission", err: &telegram.APIError{Method: "deleteMessage", HTTPStatus: 400, ErrorCode: 400, Description: "Bad Request: message can't be deleted"}},
		{name: "transport unknown", err: errors.New("timeout")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := deleteClientFunc(func(context.Context, telegram.DeleteMessageRequest) error { return test.err })
			err := (telegramDeleter{client: client}).DeleteMessage(context.Background(), 42, 103)
			if (err == nil) != test.want {
				t.Fatalf("DeleteMessage() error = %v, success=%t", err, err == nil)
			}
		})
	}
}

type deleteClientFunc func(context.Context, telegram.DeleteMessageRequest) error

func (function deleteClientFunc) DeleteMessage(ctx context.Context, request telegram.DeleteMessageRequest) error {
	return function(ctx, request)
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (function httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}
