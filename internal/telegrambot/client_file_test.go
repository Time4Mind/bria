package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientGetFileAndDirectDownload(t *testing.T) {
	fileBody := []byte("voice-payload")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/bot" + testToken + "/getFile":
			var payload getFilePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.FileID != "voice-id" {
				t.Errorf("file id = %q", payload.FileID)
			}
			writeJSON(t, writer, map[string]any{"ok": true, "result": map[string]any{
				"file_id": "voice-id", "file_unique_id": "voice-unique",
				"file_size": len(fileBody), "file_path": "voice/file_1.oga",
			}})
		case "/file/bot" + testToken + "/voice/file_1.oga":
			_, _ = writer.Write(fileBody)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	file, err := client.GetFile(context.Background(), "voice-id")
	if err != nil || file.FilePath != "voice/file_1.oga" || file.FileUniqueID != "voice-unique" {
		t.Fatalf("GetFile = (%#v, %v)", file, err)
	}
	downloaded, err := client.DownloadFile(context.Background(), file.FilePath, 100)
	if err != nil || !bytes.Equal(downloaded, fileBody) {
		t.Fatalf("DownloadFile = (%q, %v)", downloaded, err)
	}
	var streamed bytes.Buffer
	written, err := client.Download(context.Background(), "voice-id", &streamed, 100)
	if err != nil || written != int64(len(fileBody)) || !bytes.Equal(streamed.Bytes(), fileBody) {
		t.Fatalf("Download = (%d, %q, %v)", written, streamed.Bytes(), err)
	}
}

func TestClientDownloadEnforcesMetadataAndStreamingLimits(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/getFile") {
			writeJSON(t, writer, map[string]any{"ok": true, "result": map[string]any{
				"file_id": "large-id", "file_unique_id": "large-unique",
				"file_size": 11, "file_path": "documents/large.bin",
			}})
			return
		}
		downloads.Add(1)
		writer.Header().Set("Content-Length", "11")
		_, _ = writer.Write([]byte("12345678901"))
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	var destination bytes.Buffer
	written, err := client.Download(context.Background(), "large-id", &destination, 10)
	if !errors.Is(err, ErrTelegramFileTooLarge) || written != 0 || downloads.Load() != 0 {
		t.Fatalf("metadata limit = (%d, %v), downloads=%d", written, err, downloads.Load())
	}

	_, err = client.DownloadFile(context.Background(), "documents/large.bin", 10)
	if !errors.Is(err, ErrTelegramFileTooLarge) || downloads.Load() != 1 {
		t.Fatalf("response limit = %v, downloads=%d", err, downloads.Load())
	}
}

func TestClientDownloadDetectsChunkedOverflowAndShortWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = writer.Write([]byte("123456"))
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	var destination bytes.Buffer
	written, err := client.downloadFileTo(
		context.Background(), "documents/chunked.bin", &destination, 5,
	)
	if !errors.Is(err, ErrTelegramFileTooLarge) || written != 5 || destination.String() != "12345" {
		t.Fatalf("chunked overflow = (%d, %q, %v)", written, destination.String(), err)
	}

	short := shortWriter{}
	_, err = client.downloadFileTo(context.Background(), "documents/chunked.bin", short, 10)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
}

func TestClientRejectsUnsafeFilePathWithoutHTTPAndRedactsErrors(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if strings.HasSuffix(request.URL.Path, "/getFile") {
			writeJSON(t, writer, map[string]any{"ok": true, "result": map[string]any{
				"file_id": "unsafe", "file_unique_id": "u", "file_path": "../secret",
			}})
			return
		}
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := newFakeServerClient(t, server, time.Second)
	if _, err := client.GetFile(context.Background(), "unsafe"); err == nil {
		t.Fatal("GetFile accepted an unsafe path")
	}
	before := requests.Load()
	for _, path := range []string{"../secret", "/absolute", "a//b", "a?token=x", "a\\b"} {
		if _, err := client.DownloadFile(context.Background(), path, 10); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
	if requests.Load() != before {
		t.Fatal("unsafe DownloadFile performed HTTP")
	}
	_, err := client.DownloadFile(context.Background(), "safe/file.bin", 10)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("download error leaked token: %v", err)
	}
}

func TestClientDownloadRedactsTokenThroughoutTransportErrorChain(t *testing.T) {
	client, err := NewClient(ClientConfig{
		Token: testToken,
		HTTPClient: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("failed URL containing " + testToken)
		}),
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DownloadFile(context.Background(), "safe/file.bin", 10)
	if err == nil {
		t.Fatal("download unexpectedly succeeded")
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), testToken) {
			t.Fatalf("error chain leaked token: %v", current)
		}
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}
