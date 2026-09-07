package screenproduction_test

import (
	"bytes"
	"context"
	"testing"

	"bria/internal/domain"
	"bria/internal/screen"
	"bria/internal/screenproduction"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
)

type selectedScreen struct {
	id              domain.SessionID
	calls           int
	switchAfterRead bool
}

func (s *selectedScreen) LoadActiveSession(context.Context) (domain.SessionID, error) {
	s.calls++
	if s.switchAfterRead && s.calls > 1 {
		return "other", nil
	}
	return s.id, nil
}

type nativeScreens struct {
	snapshot  sessionruntime.NativeSnapshot
	calls     int
	available bool
}

func (n *nativeScreens) NativeScreen(domain.SessionID) (sessionruntime.NativeSnapshot, bool) {
	n.calls++
	return n.snapshot, n.available
}
func (n *nativeScreens) NativeScreenUpdates() <-chan domain.SessionID { return nil }

func TestNativeScreenIsOptInActiveOnlyAndUsesFullNativeCapture(t *testing.T) {
	preferences := settings.NewMemoryStore()
	selected := &selectedScreen{id: "active"}
	runtime := &nativeScreens{available: true, snapshot: sessionruntime.NativeSnapshot{Text: "cropped picker only", FullText: "actual native terminal\nselected model", Hash: "full-hash"}}
	source, err := screenproduction.NewNativeSource(preferences, selected, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if png, err := source.ScreenPNG(context.Background(), "active"); err != nil || len(png) != 0 || runtime.calls != 0 {
		t.Fatal("disabled screen captured or rendered")
	}
	if err := preferences.Update(context.Background(), func(s *settings.Settings) error { s.ScreenEnabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if png, err := source.ScreenPNG(context.Background(), "background"); err != nil || len(png) != 0 || runtime.calls != 0 {
		t.Fatal("background screen exposed")
	}
	png, err := source.ScreenPNG(context.Background(), "active")
	if err != nil {
		t.Fatal(err)
	}
	want, err := screen.RenderNative(context.Background(), runtime.snapshot.FullText)
	if err != nil || !bytes.Equal(png, want) {
		t.Fatal("render did not use exact full native screen")
	}
	png[0] = 0
	cached, err := source.ScreenPNG(context.Background(), "active")
	if err != nil || !bytes.Equal(cached, want) {
		t.Fatal("caller mutated cached PNG")
	}
	selected.calls = 0
	selected.switchAfterRead = true
	if png, err := source.ScreenPNG(context.Background(), "active"); err != nil || len(png) != 0 {
		t.Fatal("switch during snapshot leaked prior terminal")
	}
}

func TestNativeScreenCachesThreePNGsReusesTelegramFileIDAndForgetsLifecycle(t *testing.T) {
	preferences := settings.NewMemoryStore()
	if err := preferences.Update(context.Background(), func(s *settings.Settings) error {
		s.ScreenEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	selected := &selectedScreen{id: "active"}
	runtime := &nativeScreens{available: true}
	source, err := screenproduction.NewNativeSource(preferences, selected, runtime)
	if err != nil {
		t.Fatal(err)
	}

	runtime.snapshot = sessionruntime.NativeSnapshot{FullText: "first screen", Hash: "raw-1"}
	firstPNG, err := source.ScreenPNG(context.Background(), "active")
	first := source.CachedScreen("active")
	if err != nil || !bytes.Equal(firstPNG, first.PNG) || len(first.PNG) == 0 || len(first.Hash) != 64 || first.FileID != "" {
		t.Fatalf("first image = %#v, err=%v", first, err)
	}
	if !source.RememberTelegramFileID("active", first.Hash, "telegram-file-1") {
		t.Fatal("confirmed Telegram file id was not attached to exact PNG")
	}
	if got := source.CachedScreen("active"); got.Hash != first.Hash || got.FileID != "telegram-file-1" || !bytes.Equal(got.PNG, first.PNG) {
		t.Fatalf("cached confirmed image = %#v", got)
	}

	// A changed native hash which renders to identical pixels may reuse only
	// the Telegram reference for the identical final PNG hash.
	runtime.snapshot.Hash = "raw-2"
	_, err = source.ScreenPNG(context.Background(), "active")
	samePixels := source.CachedScreen("active")
	if err != nil || samePixels.Hash != first.Hash || samePixels.FileID != "telegram-file-1" {
		t.Fatalf("same PNG reuse = %#v, err=%v", samePixels, err)
	}

	for index, text := range []string{"second screen", "third screen", "fourth screen"} {
		runtime.snapshot = sessionruntime.NativeSnapshot{FullText: text, Hash: "raw-new-" + string(rune('1'+index))}
		_, renderErr := source.ScreenPNG(context.Background(), "active")
		image := source.CachedScreen("active")
		if renderErr != nil || len(image.PNG) == 0 {
			t.Fatalf("render %d = %#v, err=%v", index, image, renderErr)
		}
	}

	// The earlier PNG is beyond the three-entry session cache and therefore
	// cannot inherit its old Telegram reference.
	runtime.snapshot = sessionruntime.NativeSnapshot{FullText: "first screen", Hash: "raw-return"}
	_, err = source.ScreenPNG(context.Background(), "active")
	afterEviction := source.CachedScreen("active")
	if err != nil || afterEviction.Hash != first.Hash || afterEviction.FileID != "" {
		t.Fatalf("evicted image = %#v, err=%v", afterEviction, err)
	}

	if !source.RememberTelegramFileID("active", afterEviction.Hash, "telegram-file-return") {
		t.Fatal("reintroduced PNG was not cached")
	}
	source.ForgetSession("active")
	if cached := source.CachedScreen("active"); len(cached.PNG) != 0 || cached.Hash != "" || cached.FileID != "" {
		t.Fatalf("forgotten lifecycle cache = %#v", cached)
	}
	_, err = source.ScreenPNG(context.Background(), "active")
	afterForget := source.CachedScreen("active")
	if err != nil || len(afterForget.PNG) == 0 || afterForget.FileID != "" {
		t.Fatalf("image after lifecycle clear = %#v, err=%v", afterForget, err)
	}
	if source.RememberTelegramFileID("active", "stale-hash", "stale-file") {
		t.Fatal("stale Telegram receipt was accepted")
	}
}
