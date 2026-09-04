package providerquota

import (
	"context"
	"fmt"
	"testing"
	"time"

	"bria/internal/domain"
)

func TestParseCodexRateLimits(t *testing.T) {
	reset := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	input := fmt.Sprintf(`{"id":1,"result":{"rateLimits":{"primary":{"usedPercent":12,"windowDurationMins":300},"secondary":{"usedPercent":50,"windowDurationMins":10080,"resetsAt":%d}}}}`, reset.Unix())
	snapshot, err := parseCodex([]byte(input+"\n"), "local", time.Unix(1, 0).UTC())
	if err != nil || snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 12 || snapshot.Weekly == nil || snapshot.Weekly.UsedPercent != 50 {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
}

func TestParseCodexSelectsAccountBucketByMetadata(t *testing.T) {
	input := `{"id":1,"result":{"rateLimitsByLimitId":{` +
		`"model":{"limitName":"GPT model","primary":{"usedPercent":91,"windowDurationMins":300}},` +
		`"account":{"limitId":"codex","primary":{"usedPercent":17,"windowDurationMins":300}}}}}`
	snapshot, err := parseCodex([]byte(input+"\n"), "local", time.Unix(1, 0).UTC())
	if err != nil || snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 17 {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
}

func TestBoundedBufferReportsOverflowWithoutShortWrite(t *testing.T) {
	buffer := &boundedBuffer{limit: 3}
	n, err := buffer.Write([]byte("hello"))
	if err != nil || n != 5 || !buffer.exceeded || buffer.String() != "hel" {
		t.Fatalf("write=(%d,%v) exceeded=%v data=%q", n, err, buffer.exceeded, buffer.String())
	}
}

func TestNewExposesEveryConfiguredProviderBeforeFirstCollection(t *testing.T) {
	service, err := New("local", []Command{
		{Provider: domain.ProviderCodex, Executable: "/codex", Environment: []string{"PATH=/bin", "HOME=/tmp"}},
		{Provider: domain.ProviderClaude, Executable: "/claude", Environment: []string{"PATH=/bin", "HOME=/tmp"}},
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := service.Snapshots(context.Background())
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("snapshots=%#v err=%v", snapshots, err)
	}
}
