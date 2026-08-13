package inbound

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type bytesDownloader struct {
	data       []byte
	calls      int
	lastFileID string
	lastLimit  int64
	failures   int
}

func (d *bytesDownloader) Download(
	_ context.Context,
	fileID string,
	destination io.Writer,
	maxBytes int64,
) (int64, error) {
	d.calls++
	d.lastFileID = fileID
	d.lastLimit = maxBytes
	if d.failures > 0 {
		d.failures--
		_, _ = destination.Write([]byte("partial"))
		return 7, errors.New("temporary download failure")
	}
	written, err := destination.Write(d.data)
	return int64(written), err
}

func TestProcessorRetriesDownloadWithoutKeepingPartialBytes(t *testing.T) {
	workdir := t.TempDir()
	downloader := &bytesDownloader{data: []byte("complete"), failures: 2}
	processor, err := NewProcessor(ProcessorConfig{Downloader: downloader, MaxDownloadBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.Process(context.Background(), workdir, Input{
		Kind: KindDocument, FileID: "file", UniqueID: "unique", FileName: "x.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workdir, result.RelativePath))
	if err != nil || string(content) != "complete" || downloader.calls != 3 {
		t.Fatalf("content=%q calls=%d err=%v", content, downloader.calls, err)
	}
}

type recordingTranscriber struct {
	path string
	data []byte
	err  error
}

func (t *recordingTranscriber) Transcribe(_ context.Context, path string) (string, error) {
	t.path = path
	t.data, _ = os.ReadFile(path)
	if t.err != nil {
		return "", t.err
	}
	return "  recognized voice  ", nil
}

func TestProcessorStoresPersistentMediaSecurelyAndIdempotently(t *testing.T) {
	workdir := t.TempDir()
	downloader := &bytesDownloader{data: []byte("document")}
	processor, err := NewProcessor(ProcessorConfig{Downloader: downloader, MaxDownloadBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	input := Input{
		Kind: KindDocument, Text: "caption", FileID: "telegram-file",
		UniqueID: "stable-id", FileName: "../../report.PDF", Size: 8,
	}
	first, err := processor.Process(context.Background(), workdir, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := processor.Process(context.Background(), workdir, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.RelativePath != second.RelativePath || downloader.calls != 1 {
		t.Fatalf("not idempotent: first=%#v second=%#v calls=%d", first, second, downloader.calls)
	}
	if first.Caption != "caption" || filepath.IsAbs(first.RelativePath) ||
		strings.Contains(first.RelativePath, "..") || downloader.lastFileID != "telegram-file" ||
		downloader.lastLimit != 100 {
		t.Fatalf("unexpected result: %#v", first)
	}
	path := filepath.Join(workdir, first.RelativePath)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "document" {
		t.Fatalf("stored content = %q, err=%v", data, err)
	}
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
}

func TestProcessorRejectsSymlinkInbox(t *testing.T) {
	workdir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workdir, ".bria-inbox")); err != nil {
		t.Fatal(err)
	}
	processor, err := NewProcessor(ProcessorConfig{Downloader: &bytesDownloader{data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.Process(context.Background(), workdir, Input{
		Kind: KindPhoto, FileID: "file", UniqueID: "unique",
	})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("got %v, want unsafe path", err)
	}
}

func TestProcessorEnforcesDownloadLimitAndCleansPartialFile(t *testing.T) {
	workdir := t.TempDir()
	processor, err := NewProcessor(ProcessorConfig{
		Downloader: &bytesDownloader{data: []byte("too large")}, MaxDownloadBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.Process(context.Background(), workdir, Input{
		Kind: KindDocument, FileID: "file", UniqueID: "unique",
	})
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("got %v, want media too large", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(workdir, ".bria-inbox"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("partial files remain: %v, err=%v", entries, readErr)
	}
}

func TestProcessorVoiceIsLocalAndTemporary(t *testing.T) {
	workdir := t.TempDir()
	tempDir := t.TempDir()
	transcriber := &recordingTranscriber{}
	processor, err := NewProcessor(ProcessorConfig{
		Downloader:  &bytesDownloader{data: []byte("ogg")},
		Transcriber: transcriber, MaxDownloadBytes: 10, TempDir: tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.Process(context.Background(), workdir, Input{
		Kind: KindVoice, FileID: "voice", UniqueID: "voice-unique", Size: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "recognized voice" || string(transcriber.data) != "ogg" {
		t.Fatalf("unexpected voice result: %#v, data=%q", result, transcriber.data)
	}
	if _, err := os.Stat(transcriber.path); !os.IsNotExist(err) {
		t.Fatalf("voice temporary file remains: %v", err)
	}
}

func TestProcessorCleansVoiceWhenTranscriptionFails(t *testing.T) {
	transcriber := &recordingTranscriber{err: errors.New("failed")}
	processor, err := NewProcessor(ProcessorConfig{
		Downloader:  &bytesDownloader{data: []byte("ogg")},
		Transcriber: transcriber, TempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.Process(context.Background(), t.TempDir(), Input{
		Kind: KindVoice, FileID: "voice", UniqueID: "voice-unique",
	})
	if err == nil {
		t.Fatal("expected transcription error")
	}
	if _, statErr := os.Stat(transcriber.path); !os.IsNotExist(statErr) {
		t.Fatalf("voice temporary file remains: %v", statErr)
	}
}

func TestProcessorRejectsInvalidWorkdirAndMetadataSize(t *testing.T) {
	processor, err := NewProcessor(ProcessorConfig{
		Downloader: &bytesDownloader{}, MaxDownloadBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		workdir string
		input   Input
		want    error
	}{
		{"relative", Input{Kind: KindText}, ErrInvalidInput},
		{t.TempDir(), Input{Kind: KindPhoto, FileID: "f", UniqueID: "u", Size: 5}, ErrMediaTooLarge},
	} {
		if _, err := processor.Process(context.Background(), test.workdir, test.input); !errors.Is(err, test.want) {
			t.Errorf("got %v, want %v", err, test.want)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
