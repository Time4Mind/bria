package nativescreencache_test

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/nativerender"
	"bria/internal/nativescreencache"
)

func TestCurrentSnapshotReencodesWhenImageProfileChanges(t *testing.T) {
	profile := nativerender.ImageProfileFull8
	source, err := nativescreencache.New(nativescreencache.Config{
		Preferences: func(context.Context) (nativescreencache.Preferences, error) {
			return nativescreencache.Preferences{ScreenEnabled: true, ScreenCaptureLimitKiB: 48, ImageProfile: profile}, nil
		},
		ActiveSession: func(context.Context) (domain.SessionID, error) { return "active", nil },
		Snapshot: func(domain.SessionID) (nativescreencache.Snapshot, bool) {
			return nativescreencache.Snapshot{FullText: "same terminal frame", Hash: "same-snapshot"}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, hash, _, err := source.CurrentScreenDelivery(context.Background(), "active")
	if err != nil {
		t.Fatal(err)
	}
	if !source.RememberTelegramFileID("active", hash, "full-eight-receipt") {
		t.Fatal("receipt rejected")
	}
	profile = nativerender.ImageProfileCompact8
	second, nextHash, fileID, err := source.CurrentScreenDelivery(context.Background(), "active")
	if err != nil {
		t.Fatal(err)
	}
	a, err := png.DecodeConfig(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	b, err := png.DecodeConfig(bytes.NewReader(second))
	if err != nil {
		t.Fatal(err)
	}
	if b.Width != a.Width*3/4 || b.Height != a.Height*3/4 || hash == nextHash || fileID != "" {
		t.Fatalf("profile change reused stale PNG/receipt: full=%dx%d compact=%dx%d hashes_equal=%t file_id=%q", a.Width, a.Height, b.Width, b.Height, hash == nextHash, fileID)
	}
}

func TestEveryChangedSnapshotCanRenderWithoutIndependentCadence(t *testing.T) {
	frame := ""
	source, err := nativescreencache.New(nativescreencache.Config{
		Preferences: func(context.Context) (nativescreencache.Preferences, error) {
			return nativescreencache.Preferences{ScreenEnabled: true}, nil
		},
		ActiveSession: func(context.Context) (domain.SessionID, error) { return "active", nil },
		Snapshot: func(domain.SessionID) (nativescreencache.Snapshot, bool) {
			return nativescreencache.Snapshot{FullText: frame, Hash: frame}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := ""
	for i := 0; i < 32; i++ {
		frame = fmt.Sprintf("immediate frame %d", i)
		png, hash, fileID, err := source.CurrentScreenDelivery(context.Background(), "active")
		if err != nil || len(png) == 0 || hash == previous || fileID != "" {
			t.Fatalf("frame %d not current: hash=%s err=%v", i, hash, err)
		}
		previous = hash
	}
}

func TestCurrentSnapshotReencodesWhenCaptureLimitChanges(t *testing.T) {
	limit := 48
	source, err := nativescreencache.New(nativescreencache.Config{
		Preferences: func(context.Context) (nativescreencache.Preferences, error) {
			return nativescreencache.Preferences{ScreenEnabled: true, ScreenCaptureLimitKiB: limit}, nil
		},
		ActiveSession: func(context.Context) (domain.SessionID, error) { return "active", nil },
		Snapshot: func(domain.SessionID) (nativescreencache.Snapshot, bool) {
			return nativescreencache.Snapshot{FullText: strings.Repeat("unchanged terminal row\n", 80), Hash: "same-snapshot"}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, hash, _, err := source.CurrentScreenDelivery(context.Background(), "active")
	if err != nil {
		t.Fatal(err)
	}
	if !source.RememberTelegramFileID("active", hash, "small-crop") {
		t.Fatal("receipt rejected")
	}
	limit = 86
	second, nextHash, fileID, err := source.CurrentScreenDelivery(context.Background(), "active")
	if err != nil {
		t.Fatal(err)
	}
	a, err := png.DecodeConfig(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	b, err := png.DecodeConfig(bytes.NewReader(second))
	if err != nil {
		t.Fatal(err)
	}
	if b.Height <= a.Height || hash == nextHash || fileID != "" {
		t.Fatal("new capture limit reused old cropped PNG/receipt")
	}
}
