package processenv_test

import (
	"reflect"
	"strings"
	"testing"

	"bria/internal/processenv"
)

func TestBuildPreservesUnrelatedEnvironmentAndStripsBriaAndTelegramToken(t *testing.T) {
	parent := []string{
		"HOME=/Users/artem",
		"PATH=/usr/local/bin:/usr/bin",
		"CLAUDE_CODE_OAUTH_TOKEN=provider-auth-sentinel",
		"PROVIDER-AUTH=non-shell-name-is-still-valid",
		"LANG=ru_RU.UTF-8",
		"BRIA_SESSION_ID=logical-session",
		"BRIA_INTERNAL_SECRET=must-not-reach-provider",
		"TELEGRAM_BOT_TOKEN=must-not-reach-provider",
	}

	got, err := processenv.Build(parent, processenv.Options{TelegramTokenEnv: "TELEGRAM_BOT_TOKEN"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	want := []string{
		"HOME=/Users/artem",
		"PATH=/usr/local/bin:/usr/bin",
		"CLAUDE_CODE_OAUTH_TOKEN=provider-auth-sentinel",
		"PROVIDER-AUTH=non-shell-name-is-still-valid",
		"LANG=ru_RU.UTF-8",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build() = %#v, want %#v", got, want)
	}

	parent[0] = "HOME=/changed-input"
	got[1] = "PATH=/changed-output"
	if parent[1] != "PATH=/usr/local/bin:/usr/bin" {
		t.Fatalf("Build() result aliases input slice: parent[1] = %q", parent[1])
	}
	if got[0] != "HOME=/Users/artem" {
		t.Fatalf("Build() input aliases result slice: got[0] = %q", got[0])
	}
}

func TestBuildRejectsDuplicateBeforeFilteringWithoutLeakingValues(t *testing.T) {
	const firstSecret = "first-private-value"
	const secondSecret = "second-private-value"
	_, err := processenv.Build([]string{
		"HOME=/Users/artem",
		"PATH=/usr/bin",
		"BRIA_SECRET=" + firstSecret,
		"BRIA_SECRET=" + secondSecret,
	}, processenv.Options{GOOS: "linux"})
	if err == nil {
		t.Fatal("Build() error = nil, want duplicate rejection")
	}
	message := err.Error()
	if !strings.Contains(message, "BRIA_SECRET") {
		t.Fatalf("Build() error = %q, want offending key", message)
	}
	for _, secret := range []string{firstSecret, secondSecret} {
		if strings.Contains(message, secret) {
			t.Fatalf("Build() error leaks value %q: %q", secret, message)
		}
	}
}

func TestBuildRejectsMalformedEntriesWithoutLeakingValues(t *testing.T) {
	tests := []struct {
		name   string
		entry  string
		secret string
	}{
		{name: "missing delimiter", entry: "PROVIDER_SECRET", secret: "PROVIDER_SECRET"},
		{name: "empty key", entry: "=private-value", secret: "private-value"},
		{name: "nul in value", entry: "PROVIDER_SECRET=private\x00value", secret: "private\x00value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := processenv.Build([]string{
				"HOME=/Users/artem",
				"PATH=/usr/bin",
				test.entry,
			}, processenv.Options{GOOS: "linux"})
			if err == nil {
				t.Fatal("Build() error = nil, want malformed entry rejection")
			}
			if test.name != "missing delimiter" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("Build() error leaks value: %q", err)
			}
		})
	}
}

func TestBuildRequiresHomeAndPath(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		key  string
	}{
		{name: "home absent", env: []string{"PATH=/usr/bin"}, key: "HOME"},
		{name: "path absent", env: []string{"HOME=/Users/artem"}, key: "PATH"},
		{name: "home selected as token", env: []string{"HOME=/Users/artem", "PATH=/usr/bin"}, key: "HOME"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := processenv.Options{GOOS: "linux"}
			if test.name == "home selected as token" {
				options.TelegramTokenEnv = "HOME"
			}
			_, err := processenv.Build(test.env, options)
			if err == nil {
				t.Fatal("Build() error = nil, want required key rejection")
			}
			if !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Build() error = %q, want missing key %q", err, test.key)
			}
		})
	}
}

func TestBuildUsesCaseInsensitiveWindowsKeys(t *testing.T) {
	_, err := processenv.Build([]string{
		"Home=C:\\Users\\artem",
		"Path=C:\\Windows\\System32",
		"PROVIDER_AUTH=preserved",
		"provider_auth=ambiguous",
	}, processenv.Options{GOOS: "windows"})
	if err == nil || !strings.Contains(err.Error(), "provider_auth") {
		t.Fatalf("Build() error = %v, want case-insensitive duplicate rejection", err)
	}

	got, err := processenv.Build([]string{
		"Home=C:\\Users\\artem",
		"Path=C:\\Windows\\System32",
		"provider_auth=preserved",
		"bria_secret=removed",
		"telegram_bot_token=removed",
	}, processenv.Options{TelegramTokenEnv: "TELEGRAM_BOT_TOKEN", GOOS: "windows"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	want := []string{
		"Home=C:\\Users\\artem",
		"Path=C:\\Windows\\System32",
		"provider_auth=preserved",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build() = %#v, want %#v", got, want)
	}
}

func TestBuildRejectsMalformedTelegramTokenKey(t *testing.T) {
	_, err := processenv.Build([]string{"HOME=/Users/artem", "PATH=/usr/bin"}, processenv.Options{
		TelegramTokenEnv: "BAD=TOKEN",
		GOOS:             "linux",
	})
	if err == nil || !strings.Contains(err.Error(), "Telegram token environment key") {
		t.Fatalf("Build() error = %v, want token key rejection", err)
	}
}
