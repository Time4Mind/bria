package nativeadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
)

func TestClaudePhotoUsesVerifiedPrivateTypedCopyAndCleansIt(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "photo")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	attachment := runtimeprotocol.LocalAttachment{Path: path, Size: int64(data.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))}
	a := &adapter{config: Config{Provider: domain.ProviderClaude}}
	defer a.cleanupAttachments()
	text, err := a.inputText(context.Background(), runtimeprotocol.ParentMessage{Text: "inspect photo", Attachments: []runtimeprotocol.LocalAttachment{attachment}})
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(a.mediaDir, attachment.SHA256+".png")
	if !strings.Contains(text, "Attached image: \""+staged+"\"") || strings.Contains(text, path) {
		t.Fatalf("native photo path not typed/private: %q", text)
	}
	copy, err := os.ReadFile(staged)
	if err != nil || !bytes.Equal(copy, data.Bytes()) {
		t.Fatal("staged photo changed")
	}
	info, _ := os.Stat(staged)
	directory, _ := os.Stat(a.mediaDir)
	if info.Mode().Perm() != 0400 || directory.Mode().Perm() != 0700 {
		t.Fatal("staged photo permissions unsafe")
	}
	if repeated, err := a.inputText(context.Background(), runtimeprotocol.ParentMessage{Text: "inspect photo", Attachments: []runtimeprotocol.LocalAttachment{attachment}}); err != nil || repeated != text {
		t.Fatal("photo staging is not idempotent")
	}
	dir := a.mediaDir
	if err := a.cleanupAttachments(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("native photo directory leaked")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("custody original removed")
	}
}

func TestClaudeRejectsUnsupportedPhotoWithoutCreatingNativeInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pretend.png")
	data := []byte("not an image")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	a := &adapter{config: Config{Provider: domain.ProviderClaude}}
	text, err := a.inputText(context.Background(), runtimeprotocol.ParentMessage{Attachments: []runtimeprotocol.LocalAttachment{{Path: path, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}}})
	if err == nil || text != "" || a.mediaDir != "" {
		t.Fatal("unsupported file treated as photo success")
	}
}
