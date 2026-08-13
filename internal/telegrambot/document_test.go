package telegrambot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendDocumentStreamsMultipart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/sendDocument") {
			t.Fatalf("path=%q", request.URL.Path)
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := request.FormFile("document")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		body, err := io.ReadAll(file)
		if err != nil || string(body) != "report" || header.Filename != "report.txt" ||
			request.FormValue("chat_id") != "42" {
			t.Fatalf("file=%q name=%q chat=%q err=%v", body, header.Filename, request.FormValue("chat_id"), err)
		}
		writeJSON(t, writer, map[string]any{
			"ok": true, "result": map[string]any{"message_id": 11, "chat": map[string]any{"id": 42}},
		})
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	message, err := client.SendDocument(context.Background(), DocumentRequest{
		ChatID: 42, Name: "report.txt", MIMEType: "text/plain", Size: 6,
		Content: strings.NewReader("report"),
	})
	if err != nil || message.MessageID != 11 {
		t.Fatalf("message=%#v err=%v", message, err)
	}
}
