package telegramui

import "testing"

func TestAllowsDM(t *testing.T) {
	tests := []struct {
		name  string
		scope ChatScope
		want  bool
	}{
		{
			name:  "matching private chat",
			scope: ChatScope{Kind: ChatPrivate, ChatID: 42, UserID: 42},
			want:  true,
		},
		{
			name:  "group",
			scope: ChatScope{Kind: "group", ChatID: -100, UserID: 42},
		},
		{
			name:  "channel",
			scope: ChatScope{Kind: "channel", ChatID: -200, UserID: 42},
		},
		{
			name:  "different private user",
			scope: ChatScope{Kind: ChatPrivate, ChatID: 7, UserID: 42},
		},
		{
			name:  "missing identity",
			scope: ChatScope{Kind: ChatPrivate},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AllowsDM(test.scope); got != test.want {
				t.Fatalf("AllowsDM(%#v) = %v, want %v", test.scope, got, test.want)
			}
		})
	}
}
