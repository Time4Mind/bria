package providerinputcomposition_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/providerinputcomposition"
	"bria/internal/sessionruntime"
	"bria/internal/turnprocessing"
)

func TestClaudeUnsupportedBinaryNeverReachesMainSession(t *testing.T) {
	data := []byte("not an image, despite png extension")
	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	runtime := &structuredRuntime{}
	resolver := &attachmentResolver{paths: map[string]string{"photo": path}}
	providers := &sessionProviders{providers: map[domain.SessionID]domain.Provider{"claude": domain.ProviderClaude}}
	submitter, err := providerinputcomposition.New(runtime, resolver, providers)
	if err != nil {
		t.Fatal(err)
	}
	_, err = submitter.SubmitPreparedWithCallbacks(context.Background(), "claude", turnprocessing.PreparedInput{Text: "inspect", Attachments: []turnprocessing.AttachmentRef{{Reference: "photo", Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}}}, sessionruntime.TurnCallbacks{MessageID: "m"})
	if !errors.Is(err, providerinputcomposition.ErrProviderAttachmentsUnsupported) || runtime.calls != 0 {
		t.Fatalf("unsupported binary dispatched calls=%d err=%v", runtime.calls, err)
	}
}
