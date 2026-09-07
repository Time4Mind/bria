package runtimeprotocol

import (
	"strings"
	"testing"
)

func TestNativeWireBoundaries(t *testing.T) {
	for _, key := range []string{"", "up", "down", "left", "right", "enter", "escape", "tab", "space"} {
		message := ParentMessage{Protocol: Version, Type: TypeNativeControl, RequestID: "n-1", Key: key}
		line, err := EncodeParentLine(message, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeParentLine(line, Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, message := range []ParentMessage{
		{Protocol: Version, Type: TypeNativeControl, RequestID: "n-1", Command: "x", Key: "enter"},
		{Protocol: Version, Type: TypeNativeControl, RequestID: "n-1", Key: "evil"},
		{Protocol: Version, Type: TypeNativeControl, RequestID: "n-1", Command: "x\x1b"},
		{Protocol: Version, Type: TypeNativeControl, RequestID: "n-1", ExpectedHash: "bad"},
		{Protocol: Version, Type: TypeSubmit, RequestID: "r-1", Command: "/model"},
	} {
		if _, err := EncodeParentLine(message, Limits{}); err == nil {
			t.Fatalf("accepted %+v", message)
		}
	}
	if _, err := DecodeParentLine([]byte(`{"protocol":1,"type":"native_control","request_id":"n-1","text":"hidden"}`), Limits{}); err == nil {
		t.Fatal("cross-type field accepted")
	}
	message := AdapterMessage{Protocol: Version, Type: TypeNativeSnapshot, RequestID: "n-1", Hash: strings.Repeat("a", 64), ErrorCode: "stale"}
	line, err := EncodeAdapterLine(message, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAdapterLine(line, Limits{}); err != nil {
		t.Fatal(err)
	}
	message.ErrorCode = "arbitrary"
	if _, err := EncodeAdapterLine(message, Limits{}); err == nil {
		t.Fatal("unknown error accepted")
	}
}

func TestReadyOptionalModelIsBoundedAndBackwardCompatible(t *testing.T) {
	for _, model := range []string{"", "actual-cli-model"} {
		message := AdapterMessage{Protocol: Version, Type: TypeReady, ProviderSessionID: "session", Readiness: "protocol", Authentication: "unknown", Model: model}
		line, err := EncodeAdapterLine(message, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeAdapterLine(line, Limits{})
		if err != nil || decoded.Model != model {
			t.Fatal("optional ready model roundtrip failed")
		}
	}
	message := AdapterMessage{Protocol: Version, Type: TypeReady, ProviderSessionID: "session", Readiness: "protocol", Authentication: "unknown", Model: strings.Repeat("m", 257)}
	if _, err := EncodeAdapterLine(message, Limits{}); err == nil {
		t.Fatal("oversized startup model accepted")
	}
	message.Type = TypeAccepted
	message.Model = "model"
	message.RequestID = "r-1"
	if _, err := EncodeAdapterLine(message, Limits{}); err == nil {
		t.Fatal("model allowed on unrelated wire type")
	}
}

func TestNativeFullScreenBoundsAndTypedTool(t *testing.T) {
	message := AdapterMessage{Protocol: Version, Type: TypeNativeSnapshot, RequestID: "r", Hash: strings.Repeat("a", 64), Text: "cropped", FullText: "full terminal"}
	line, err := EncodeAdapterLine(message, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAdapterLine(line, Limits{})
	if err != nil || decoded.FullText != message.FullText {
		t.Fatal("full terminal lost")
	}
	message.FullText = strings.Repeat("x", 24<<10+1)
	if _, err := EncodeAdapterLine(message, Limits{}); err == nil {
		t.Fatal("unbounded full screen accepted")
	}
	message = AdapterMessage{Protocol: Version, Type: TypeEvent, RequestID: "r", Kind: "tool", Text: "tool-name"}
	line, err = EncodeAdapterLine(message, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeAdapterLine(line, Limits{})
	if err != nil || decoded.Kind != "tool" {
		t.Fatal("typed tool not preserved")
	}
}
