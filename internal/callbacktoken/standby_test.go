package callbacktoken

import (
	"bytes"
	"testing"
	"time"
)

func TestStandbyUsesNewGlobalWireIDAndNoTarget(t *testing.T) {
	if ActionSettingsStandby != 84 {
		t.Fatal("standby reused an existing wire ID")
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{1}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	fields := Fields{Action: ActionSettingsStandby, SessionID: "00000000-0000-0000-0000-000000000001", ExpiresAt: now.Add(time.Minute)}
	token, err := codec.Encode(fields)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(token)
	if err != nil || got != fields {
		t.Fatalf("fields=%#v error=%v", got, err)
	}
	fields.Target = 1
	if _, err := codec.Encode(fields); err == nil {
		t.Fatal("standby target accepted")
	}
}

func TestLateSettingsActionsUseDistinctGlobalWireIDs(t *testing.T) {
	if ActionSettingsRenameNode != 93 || ActionSettingsAutoApproveCommands != 94 {
		t.Fatalf("late settings wire IDs = rename:%d autoapprove:%d", ActionSettingsRenameNode, ActionSettingsAutoApproveCommands)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := New(bytes.Repeat([]byte{0x42}, 32), bytes.NewReader(bytes.Repeat([]byte{1}, 256)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{ActionSettingsRenameNode, ActionSettingsAutoApproveCommands} {
		fields := Fields{Action: action, SessionID: "00000000-0000-0000-0000-000000000001", ExpiresAt: now.Add(time.Minute)}
		token, err := codec.Encode(fields)
		if err != nil {
			t.Fatalf("encode action %d: %v", action, err)
		}
		got, err := codec.Decode(token)
		if err != nil || got != fields {
			t.Fatalf("action %d fields=%#v error=%v", action, got, err)
		}
	}
}
