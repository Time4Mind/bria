package telegrambot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testToken = "123:test_token"

func newFakeServerClient(t *testing.T, server *httptest.Server, timeout time.Duration) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		Token: testToken, BaseURL: server.URL, HTTPClient: server.Client(),
		RequestTimeout: timeout, LongPollSlack: timeout, AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
