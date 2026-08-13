package telegrambot

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestExternalTelegramIdentity is opt-in and performs only getMe. It never
// starts getUpdates, so it is safe while another process owns bot polling.
func TestExternalTelegramIdentity(t *testing.T) {
	path := os.Getenv("BRIA_TELEGRAM_ENV_FILE")
	if path == "" {
		t.Skip("BRIA_TELEGRAM_ENV_FILE is not set")
	}
	token := dotenvValue(t, path, "TELEGRAM_BOT_TOKEN")
	client, err := NewClient(ClientConfig{Token: token, RequestTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	identity, err := client.GetMe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.TrimPrefix(os.Getenv("BRIA_EXPECTED_BOT_USERNAME"), "@")
	if expected != "" && identity.Username != expected {
		t.Fatalf("unexpected bot username %q", identity.Username)
	}
}

func dotenvValue(t *testing.T, path, key string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	prefix := key + "="
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		return strings.Trim(value, "\"'")
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("%s is missing from Telegram env file", key)
	return ""
}
