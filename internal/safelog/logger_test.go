package safelog_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bria/internal/safelog"
)

func TestWriteRedactsSecretsAndPrivateContentBeforePersistence(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	log, err := safelog.Open(safelog.Options{Directory: dir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	secrets := []string{
		"123456789:telegram-token-secret",
		"Bearer provider-secret",
		"PAIR-482193",
		"/Users/artem/.config/claude/auth.json",
		"please inspect my private source",
	}
	err = log.Write(safelog.Event{
		Class:         safelog.Service,
		Type:          "provider.auth",
		EntityID:      "session-17",
		Result:        "rejected",
		ErrorCategory: "authorization_failed",
		Error:         "POST https://api.telegram.org/bot123456789:telegram-token-secret/sendMessage Authorization: Bearer provider-secret failed for /Users/artem/.config/claude/auth.json?code=PAIR-482193",
		Fields: map[string]string{
			"telegram_token": "123456789:telegram-token-secret",
			"callback_code":  "PAIR-482193",
			"authorization":  "Bearer provider-secret",
			"auth_path":      "/Users/artem/.config/claude/auth.json",
			"message":        "please inspect my private source",
			"operation":      "login",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "service.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("persisted sensitive value %q: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), `"operation":"login"`) {
		t.Fatalf("safe structured field was lost: %s", raw)
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("redaction marker missing: %s", raw)
	}
}

func TestWriteFailsClosedForUnstructuredContent(t *testing.T) {
	dir := t.TempDir()
	log, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}

	privateError := "the owner asked me to inspect project zephyr"
	privateField := "agent answered with unreleased acquisition details"
	privateKey := "private customer message from telegram"
	secretLookingLikeAFieldName := "supersecretvalue"
	if err := log.Write(safelog.Event{
		Class: safelog.Service,
		Type:  "provider.failure",
		Error: privateError,
		Fields: map[string]string{
			"operation":                 "submit",
			"detail":                    privateField,
			privateKey:                  "otherwise harmless",
			"response":                  "arbitrary agent response",
			"photo_bytes":               "raw photo content",
			secretLookingLikeAFieldName: "otherwise harmless too",
		},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "service.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{
		privateError,
		privateField,
		privateKey,
		secretLookingLikeAFieldName,
		"arbitrary agent response",
		"raw photo content",
	} {
		if strings.Contains(string(raw), sensitive) {
			t.Fatalf("persisted unstructured content %q: %s", sensitive, raw)
		}
	}
	if !strings.Contains(string(raw), `"operation":"submit"`) {
		t.Fatalf("safe structured field was lost: %s", raw)
	}
}

func TestWriteTimeCannotBeSuppliedByCaller(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	log, err := safelog.Open(safelog.Options{Directory: dir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	if err := log.Write(safelog.Event{
		Class: safelog.Detailed,
		Type:  "measurement",
		Time:  now.Add(365 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	records, err := log.Read(safelog.Detailed)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !records[0].Time.Equal(now) {
		t.Fatalf("write timestamp = %v, want %v", records, now)
	}
}

func TestCleanupAppliesClassSpecificRetentionAtBoundary(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	current := base
	log, err := safelog.Open(safelog.Options{Directory: dir, Now: func() time.Time { return current }})
	if err != nil {
		t.Fatal(err)
	}

	for _, class := range []safelog.Class{safelog.Detailed, safelog.Service, safelog.Critical} {
		if err := log.Write(safelog.Event{Class: class, Type: "old", Time: base}); err != nil {
			t.Fatal(err)
		}
	}
	current = base.Add(6*time.Hour - time.Nanosecond)
	if err := log.Write(safelog.Event{Class: safelog.Detailed, Type: "young", Time: current}); err != nil {
		t.Fatal(err)
	}
	current = base.Add(6 * time.Hour)
	if err := log.Cleanup(); err != nil {
		t.Fatal(err)
	}

	detailed, err := log.Read(safelog.Detailed)
	if err != nil {
		t.Fatal(err)
	}
	if len(detailed) != 1 || detailed[0].Type != "young" {
		t.Fatalf("detailed retention mismatch: %+v", detailed)
	}
	service, err := log.Read(safelog.Service)
	if err != nil {
		t.Fatal(err)
	}
	if len(service) != 1 {
		t.Fatalf("service event expired before 24h: %+v", service)
	}
	critical, err := log.Read(safelog.Critical)
	if err != nil {
		t.Fatal(err)
	}
	if len(critical) != 1 {
		t.Fatalf("critical event expired before 72h: %+v", critical)
	}

	current = base.Add(72 * time.Hour)
	if err := log.Cleanup(); err != nil {
		t.Fatal(err)
	}
	for _, class := range []safelog.Class{safelog.Detailed, safelog.Service, safelog.Critical} {
		records, err := log.Read(class)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 0 {
			t.Fatalf("%s event survived its retention: %+v", class, records)
		}
	}
}

func TestRunCleanupImmediatelyExpiresRecordsWithoutNewLogTraffic(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	current := base
	log, err := safelog.Open(safelog.Options{Directory: dir, Now: func() time.Time { return current }})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Write(safelog.Event{Class: safelog.Detailed, Type: "measurement"}); err != nil {
		t.Fatal(err)
	}
	current = base.Add(6 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := log.RunCleanup(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunCleanup() error = %v, want context cancellation", err)
	}
	records, err := log.Read(safelog.Detailed)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("expired records survived supervised cleanup: %+v", records)
	}
}

func TestBoundsKeepNewestWholeRecordsAndErrorsNeverEchoInput(t *testing.T) {
	dir := t.TempDir()
	current := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	log, err := safelog.Open(safelog.Options{
		Directory:      dir,
		MaxRecords:     2,
		MaxRecordBytes: 512,
		MaxFileBytes:   1024,
		Now:            func() time.Time { return current },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"first", "second", "third"} {
		current = current.Add(time.Second)
		if err := log.Write(safelog.Event{Class: safelog.Service, Type: typ}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := log.Read(safelog.Service)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Type != "second" || records[1].Type != "third" {
		t.Fatalf("bounded order mismatch: %+v", records)
	}

	secret := "Bearer must-never-escape"
	err = log.Write(safelog.Event{Class: safelog.Service, Type: "oversized", Error: strings.Repeat(secret, 80)})
	if !errors.Is(err, safelog.ErrRecordTooLarge) {
		t.Fatalf("got %v, want ErrRecordTooLarge", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed secret: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "service.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("rejected record leaked to disk: %s", raw)
	}
}

func TestConcurrentProcessesDoNotLoseWholeRecords(t *testing.T) {
	dir := t.TempDir()
	cleaner, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	const processCount = 4
	const recordsPerProcess = 25
	type runningCommand struct {
		command *exec.Cmd
		output  *bytes.Buffer
	}
	commands := make([]runningCommand, 0, processCount)
	for process := 0; process < processCount; process++ {
		command := exec.Command(os.Args[0], "-test.run=^TestSafelogProcessWriter$")
		output := &bytes.Buffer{}
		command.Stdout = output
		command.Stderr = output
		command.Env = append(os.Environ(),
			"BRIA_SAFELOG_HELPER_DIR="+dir,
			"BRIA_SAFELOG_HELPER_INDEX="+strconv.Itoa(process),
			"BRIA_SAFELOG_HELPER_COUNT="+strconv.Itoa(recordsPerProcess),
		)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, runningCommand{command: command, output: output})
	}
	// Cleanup uses the same interprocess critical section as writes. All helper
	// records are fresh, so a racing cleanup must preserve every one of them.
	for attempt := 0; attempt < 10; attempt++ {
		if err := cleaner.Cleanup(); err != nil {
			t.Fatal(err)
		}
	}
	for _, running := range commands {
		if err := running.command.Wait(); err != nil {
			t.Fatalf("writer process failed: %v: %s", err, running.output.Bytes())
		}
	}

	log, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	records, err := log.Read(safelog.Service)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != processCount*recordsPerProcess {
		t.Fatalf("got %d records, want %d", len(records), processCount*recordsPerProcess)
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if seen[record.Type] {
			t.Fatalf("duplicate record %q", record.Type)
		}
		seen[record.Type] = true
	}
}

func TestSafelogProcessWriter(t *testing.T) {
	dir := os.Getenv("BRIA_SAFELOG_HELPER_DIR")
	if dir == "" {
		return
	}
	index, err := strconv.Atoi(os.Getenv("BRIA_SAFELOG_HELPER_INDEX"))
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(os.Getenv("BRIA_SAFELOG_HELPER_COUNT"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	for record := 0; record < count; record++ {
		if err := log.Write(safelog.Event{
			Class: safelog.Service,
			Type:  fmt.Sprintf("writer%d.%d", index, record),
		}); err != nil {
			t.Fatal(err)
		}
	}
}
