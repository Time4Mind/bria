package processmetrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalkNumericDirectoryCapsBeforeVisitingExtraEntries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"1", "2", "3", "4", "5", "not-a-pid"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	visited := 0
	count, capped, err := walkNumericDirectory(
		context.Background(), root, 3,
		func(string) error { visited++; return nil },
	)
	if err != nil || count != 3 || visited != 3 || !capped {
		t.Fatalf("count=%d visited=%d capped=%t err=%v", count, visited, capped, err)
	}
}

func TestBoundedCollectorsHonorCancellationAndChildCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := walkNumericDirectory(ctx, t.TempDir(), 3, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	children := make(map[string]struct{})
	capped, err := scanChildPIDs(
		context.Background(), strings.NewReader("11 12 11 13 14"), children, 2,
	)
	if err != nil || !capped || len(children) != 2 {
		t.Fatalf("children=%v capped=%t err=%v", children, capped, err)
	}
}

func TestRSSParsersRejectMalformedAndOverflowValues(t *testing.T) {
	if bytes, ok := parseRSSKibibytes("42\n"); !ok || bytes != 42*1024 {
		t.Fatalf("kibibytes bytes=%d ok=%t", bytes, ok)
	}
	if _, ok := parseRSSKibibytes("9223372036854775807"); ok {
		t.Fatal("overflowing KiB value accepted")
	}
	if bytes, ok := parseRSSPages("10 3 2", 4096); !ok || bytes != 3*4096 {
		t.Fatalf("pages bytes=%d ok=%t", bytes, ok)
	}
	if _, ok := parseRSSPages("1 invalid", 4096); ok {
		t.Fatal("malformed page value accepted")
	}
}
