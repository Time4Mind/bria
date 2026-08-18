package telegrambot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRichFloodWaitDoesNotAttemptTextFallback(t *testing.T) {
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		methods = append(methods, method)
		writer.WriteHeader(http.StatusTooManyRequests)
		writeJSON(t, writer, map[string]any{
			"ok": false, "error_code": 429,
			"description": "Too Many Requests: retry after 120",
			"parameters":  map[string]any{"retry_after": 120},
		})
	}))
	defer server.Close()

	client := newFakeServerClient(t, server, time.Second)
	_, err := client.SendScreen(context.Background(), 42, richTestScreen(testPanePNG(t)))
	if retryAfter, limited := FloodWait(err); !limited || retryAfter != 120*time.Second {
		t.Fatalf("FloodWait(%v)=(%v,%t)", err, retryAfter, limited)
	}
	if got := strings.Join(methods, ","); got != "sendRichMessage" {
		t.Fatalf("methods=%q want sendRichMessage only", got)
	}
}
