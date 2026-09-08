package nativecapture_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/nativecapture"
)

func TestCaptureBoundaryPreservesTailAndUTF8(t *testing.T) {
	for _, limit := range []int{0, -1, 1, 48, 64, 86, 87} {
		wantKiB := limit
		if limit != 48 && limit != 64 && limit != 86 {
			wantKiB = 48
		}
		input := strings.Repeat("Я", 90*1024) + "!"
		got := nativecapture.Bound(input, limit)
		if !utf8.ValidString(got) || len(got) != wantKiB*1024-1 || !strings.HasSuffix(input, got) {
			t.Fatalf("limit %d: bytes=%d, valid=%v", limit, len(got), utf8.ValidString(got))
		}
	}
	if got := nativecapture.Bound("short\ncontent", 48); got != "short\ncontent" {
		t.Fatalf("short screen changed: %q", got)
	}
}

func TestCaptureHashUsesExactBytes(t *testing.T) {
	if got := nativecapture.Hash("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("hash = %s", got)
	}
	if nativecapture.Hash("abc") == nativecapture.Hash("abc\n") {
		t.Fatal("screen identity discarded terminal newline")
	}
}
