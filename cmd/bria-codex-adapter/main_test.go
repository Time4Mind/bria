package main

import (
	"reflect"
	"testing"
)

func TestParseRawCommandPreservesDirectArgvAfterSeparator(t *testing.T) {
	want := []string{"/opt/codex", "app-server"}
	got, err := parseRawCommand([]string{"--", "/opt/codex", "app-server"})
	if err != nil {
		t.Fatalf("parseRawCommand() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRawCommand() = %#v, want %#v", got, want)
	}
}

func TestParseRawCommandRequiresSeparatorAndCommand(t *testing.T) {
	tests := [][]string{
		nil,
		{"--"},
		{"/opt/codex", "app-server"},
		{"prefix", "--", "/opt/codex", "app-server"},
	}
	for _, args := range tests {
		if _, err := parseRawCommand(args); err == nil {
			t.Errorf("parseRawCommand(%#v) error = nil", args)
		}
	}
}

func TestParseAdapterStartRequiresExplicitNewOrExactResume(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantID    string
		wantError bool
	}{
		{name: "new", env: map[string]string{"BRIA_START_MODE": "new"}},
		{name: "resume", env: map[string]string{"BRIA_START_MODE": "resume", "BRIA_PROVIDER_SESSION_ID": "thread-123"}, wantID: "thread-123"},
		{name: "missing mode", env: map[string]string{}, wantError: true},
		{name: "unknown mode", env: map[string]string{"BRIA_START_MODE": "continue"}, wantError: true},
		{name: "new with stale id", env: map[string]string{"BRIA_START_MODE": "new", "BRIA_PROVIDER_SESSION_ID": "thread-123"}, wantError: true},
		{name: "resume without id", env: map[string]string{"BRIA_START_MODE": "resume"}, wantError: true},
		{name: "resume with unsafe id", env: map[string]string{"BRIA_START_MODE": "resume", "BRIA_PROVIDER_SESSION_ID": "thread id"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseAdapterStart(func(key string) string { return test.env[key] })
			if (err != nil) != test.wantError {
				t.Fatalf("parseAdapterStart() error = %v, wantError %t", err, test.wantError)
			}
			if got != test.wantID {
				t.Fatalf("parseAdapterStart() = %q, want %q", got, test.wantID)
			}
		})
	}
}
