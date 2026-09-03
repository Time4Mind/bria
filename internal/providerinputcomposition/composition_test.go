package providerinputcomposition_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/providerinputcomposition"
	"bria/internal/sessionruntime"
	"bria/internal/turnprocessing"
)

func TestCodexSubmitterResolvesAndVerifiesOpaqueAttachmentsInExactOrder(t *testing.T) {
	directory := t.TempDir()
	paths := []string{filepath.Join(directory, "one.png"), filepath.Join(directory, "two.png")}
	contents := [][]byte{[]byte("first image"), []byte("second image")}
	refs := []string{"photo-custody-one", "photo-custody-two"}
	resolver := &attachmentResolver{paths: map[string]string{refs[0]: paths[0], refs[1]: paths[1]}}
	attachments := make([]turnprocessing.AttachmentRef, 2)
	for index := range paths {
		if err := os.WriteFile(paths[index], contents[index], 0o600); err != nil {
			t.Fatal(err)
		}
		attachments[index] = turnprocessing.AttachmentRef{
			Reference: refs[index], Size: int64(len(contents[index])), SHA256: fmt.Sprintf("%x", sha256.Sum256(contents[index])),
		}
	}
	runtime := &structuredRuntime{}
	submitter, err := providerinputcomposition.New(runtime, resolver, &sessionProviders{providers: map[domain.SessionID]domain.Provider{
		"session-1": domain.ProviderCodex,
	}})
	if err != nil {
		t.Fatal(err)
	}
	accepted := ""
	_, err = submitter.SubmitPreparedWithCallbacks(context.Background(), "session-1", turnprocessing.PreparedInput{
		Text: "inspect these", Attachments: attachments,
	}, sessionruntime.TurnCallbacks{MessageID: "telegram:photo:1", OnAccepted: func(messageID string) error {
		accepted = messageID
		return nil
	}})
	if err != nil {
		t.Fatalf("SubmitPreparedWithCallbacks() error = %v", err)
	}
	if !reflect.DeepEqual(resolver.resolved, refs) || accepted != "telegram:photo:1" {
		t.Fatalf("resolved=%#v accepted=%q", resolver.resolved, accepted)
	}
	if runtime.input.Text != "inspect these" || len(runtime.input.Attachments) != 2 {
		t.Fatalf("structured runtime input = %#v", runtime.input)
	}
	for index, attachment := range runtime.input.Attachments {
		if attachment.Path != paths[index] || attachment.Size != attachments[index].Size || attachment.SHA256 != attachments[index].SHA256 {
			t.Fatalf("runtime attachment %d = %#v", index, attachment)
		}
		if strings.Contains(runtime.input.Text, attachment.Path) || strings.Contains(runtime.input.Text, refs[index]) {
			t.Fatalf("attachment identity entered prompt text %q", runtime.input.Text)
		}
	}
	var _ turnprocessing.PreparedTurnSubmitter = submitter
}

func TestCodexSubmitterRejectsChangedContentBeforeRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-photo.png")
	if err := os.WriteFile(path, []byte("changed bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &structuredRuntime{}
	submitter, err := providerinputcomposition.New(runtime, &attachmentResolver{paths: map[string]string{"photo-1": path}}, &sessionProviders{providers: map[domain.SessionID]domain.Provider{
		"session-1": domain.ProviderCodex,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = submitter.SubmitPreparedWithCallbacks(context.Background(), "session-1", turnprocessing.PreparedInput{
		Text: "inspect", Attachments: []turnprocessing.AttachmentRef{{
			Reference: "photo-1", Size: int64(len("changed bytes")),
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}, sessionruntime.TurnCallbacks{MessageID: "telegram:photo:bad"})
	if !errors.Is(err, providerinputcomposition.ErrAttachmentUnverifiable) || runtime.calls != 0 {
		t.Fatalf("submit error=%v runtime calls=%d", err, runtime.calls)
	}
	if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "changed bytes") {
		t.Fatalf("sanitized error leaked attachment: %v", err)
	}
}

func TestClaudeSubmitterFailsClosedBeforeResolvingUnsupportedImage(t *testing.T) {
	resolver := &attachmentResolver{paths: map[string]string{"photo-1": "/private/tmp/never-read"}}
	runtime := &structuredRuntime{}
	providers := &sessionProviders{providers: map[domain.SessionID]domain.Provider{"session-1": domain.ProviderClaude}}
	submitter, err := providerinputcomposition.New(runtime, resolver, providers)
	if err != nil {
		t.Fatal(err)
	}
	_, err = submitter.SubmitPreparedWithCallbacks(context.Background(), "session-1", turnprocessing.PreparedInput{
		Text: "inspect", Attachments: []turnprocessing.AttachmentRef{{Reference: "photo-1", Size: 3, SHA256: strings.Repeat("a", 64)}},
	}, sessionruntime.TurnCallbacks{MessageID: "telegram:photo:claude"})
	if !errors.Is(err, providerinputcomposition.ErrProviderAttachmentsUnsupported) || len(resolver.resolved) != 0 || runtime.calls != 0 {
		t.Fatalf("submit error=%v resolved=%#v runtime calls=%d", err, resolver.resolved, runtime.calls)
	}
	if !reflect.DeepEqual(providers.requested, []domain.SessionID{"session-1"}) {
		t.Fatalf("provider lookups = %#v", providers.requested)
	}
}

func TestSubmitterRoutesMixedPhotoAndTextTurnsByLogicalSession(t *testing.T) {
	content := []byte("codex image")
	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	providers := &sessionProviders{providers: map[domain.SessionID]domain.Provider{
		"codex-photo":  domain.ProviderCodex,
		"codex-text":   domain.ProviderCodex,
		"claude-text":  domain.ProviderClaude,
		"claude-photo": domain.ProviderClaude,
	}}
	resolver := &attachmentResolver{paths: map[string]string{"custody-photo": path}}
	runtime := &structuredRuntime{}
	submitter, err := providerinputcomposition.New(runtime, resolver, providers)
	if err != nil {
		t.Fatal(err)
	}

	photo := turnprocessing.PreparedInput{Text: "inspect", Attachments: []turnprocessing.AttachmentRef{{
		Reference: "custody-photo", Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content)),
	}}}
	if _, err := submitter.SubmitPreparedWithCallbacks(context.Background(), "codex-photo", photo, sessionruntime.TurnCallbacks{MessageID: "m-1"}); err != nil {
		t.Fatalf("Codex photo: %v", err)
	}
	for _, sessionID := range []domain.SessionID{"codex-text", "claude-text"} {
		if _, err := submitter.SubmitPreparedWithCallbacks(context.Background(), sessionID, turnprocessing.PreparedInput{Text: "plain"}, sessionruntime.TurnCallbacks{MessageID: "m-2"}); err != nil {
			t.Fatalf("text %s: %v", sessionID, err)
		}
	}
	beforeRuntime := runtime.calls
	beforeCustody := len(resolver.resolved)
	_, err = submitter.SubmitPreparedWithCallbacks(context.Background(), "claude-photo", photo, sessionruntime.TurnCallbacks{MessageID: "m-3"})
	if !errors.Is(err, providerinputcomposition.ErrProviderAttachmentsUnsupported) {
		t.Fatalf("Claude photo error = %v", err)
	}
	if runtime.calls != beforeRuntime || len(resolver.resolved) != beforeCustody {
		t.Fatalf("Claude photo reached runtime/custody: runtime=%d resolved=%#v", runtime.calls, resolver.resolved)
	}
	wantLookups := []domain.SessionID{"codex-photo", "codex-text", "claude-text", "claude-photo"}
	if !reflect.DeepEqual(providers.requested, wantLookups) {
		t.Fatalf("provider lookups = %#v, want %#v", providers.requested, wantLookups)
	}
}

func TestSubmitterFailsClosedForMissingAmbiguousOrInvalidSessionProvider(t *testing.T) {
	for _, test := range []struct {
		name      string
		providers *sessionProviders
	}{
		{name: "missing", providers: &sessionProviders{err: errors.New("missing private/session/path")}},
		{name: "ambiguous", providers: &sessionProviders{err: errors.New("ambiguous secret-provider-state")}},
		{name: "invalid", providers: &sessionProviders{providers: map[domain.SessionID]domain.Provider{"session": "future"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &structuredRuntime{}
			custody := &attachmentResolver{}
			submitter, err := providerinputcomposition.New(runtime, custody, test.providers)
			if err != nil {
				t.Fatal(err)
			}
			_, err = submitter.SubmitPreparedWithCallbacks(context.Background(), "session", turnprocessing.PreparedInput{Text: "plain"}, sessionruntime.TurnCallbacks{MessageID: "m"})
			if !errors.Is(err, providerinputcomposition.ErrSessionProviderUnverifiable) || runtime.calls != 0 || len(custody.resolved) != 0 {
				t.Fatalf("error=%v runtime=%d custody=%#v", err, runtime.calls, custody.resolved)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "future") {
				t.Fatalf("error leaked provider state: %v", err)
			}
		})
	}
}

type attachmentResolver struct {
	paths    map[string]string
	resolved []string
}

func (resolver *attachmentResolver) ResolveAttachment(_ context.Context, reference string) (string, error) {
	resolver.resolved = append(resolver.resolved, reference)
	path, ok := resolver.paths[reference]
	if !ok {
		return "", errors.New("private resolver error")
	}
	return path, nil
}

type structuredRuntime struct {
	calls int
	input sessionruntime.StructuredInput
}

func (runtime *structuredRuntime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, nil
}

func (runtime *structuredRuntime) SubmitWithCallbacks(_ context.Context, _ domain.SessionID, _ string, _ sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	runtime.calls++
	return sessionruntime.TurnResult{}, nil
}

func (runtime *structuredRuntime) SubmitStructuredWithCallbacks(_ context.Context, _ domain.SessionID, input sessionruntime.StructuredInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	runtime.calls++
	runtime.input = input
	if callbacks.OnAccepted != nil {
		if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
			return sessionruntime.TurnResult{}, err
		}
	}
	return sessionruntime.TurnResult{Final: "seen", TerminalStatus: sessionruntime.StatusCompleted}, nil
}

type sessionProviders struct {
	providers map[domain.SessionID]domain.Provider
	err       error
	requested []domain.SessionID
}

func (providers *sessionProviders) ProviderForSession(_ context.Context, sessionID domain.SessionID) (domain.Provider, error) {
	providers.requested = append(providers.requested, sessionID)
	if providers.err != nil {
		return "", providers.err
	}
	return providers.providers[sessionID], nil
}
