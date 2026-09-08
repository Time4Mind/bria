package runtimeprotocol

import (
	"strings"
	"testing"
)

func TestApproveOnceRequiresValidExpectedHash(t *testing.T) {
	base := ParentMessage{Protocol: Version, Type: TypeNativeControl, RequestID: "n-approval", Key: "approve_once"}
	for _, message := range []ParentMessage{
		base,
		func() ParentMessage { m := base; m.ExpectedHash = "short"; return m }(),
		func() ParentMessage { m := base; m.ExpectedHash = strings.Repeat("g", 64); return m }(),
	} {
		if _, err := EncodeParentLine(message, Limits{}); err == nil {
			t.Fatalf("accepted invalid approve_once message: %+v", message)
		}
	}
	valid := base
	valid.ExpectedHash = strings.Repeat("a", 64)
	line, err := EncodeParentLine(valid, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeParentLine(line, Limits{})
	if err != nil || decoded.Key != "approve_once" || decoded.ExpectedHash != valid.ExpectedHash {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}
