package promptidentity

import "testing"

func TestDigestIsExactAndContentFree(t *testing.T) {
	first := Digest("long prompt\nsecond line")
	if len(first) != DigestLength {
		t.Fatalf("digest length=%d", len(first))
	}
	if first != Digest("long prompt\nsecond line") {
		t.Fatal("same prompt produced a different digest")
	}
	if first == Digest("long prompt\nsecond line ") {
		t.Fatal("different exact prompt bytes share a digest")
	}
}
