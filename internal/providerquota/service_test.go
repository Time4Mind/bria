package providerquota

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"bria/internal/domain"
)

func TestCollectCodexCLIContract(t *testing.T) {
	for _, arguments := range [][]string{nil, {"app-server"}, {"app-server", "--stdio"}} {
		t.Run(fmt.Sprint(arguments), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			snapshot, err := collectCodex(ctx, "local", Command{Executable: os.Args[0],
				Arguments:   append([]string{"-test.run=TestQuotaCLIHelper", "--"}, arguments...),
				Environment: append(os.Environ(), "BRIA_QUOTA_HELPER=1")})
			if err != nil || snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 23 {
				t.Fatalf("quota unavailable: %v snapshot=%#v", err, snapshot)
			}
		})
	}
}

func TestQuotaCLIHelper(t *testing.T) {
	if os.Getenv("BRIA_QUOTA_HELPER") != "1" {
		return
	}
	if os.Getenv("BRIA_QUOTA_STALL") == "1" {
		time.Sleep(30 * time.Second)
		os.Exit(24)
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if !slices.Equal(args, []string{"app-server", "--stdio"}) {
		os.Exit(21)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for _, method := range []string{"initialize", "initialized", "account/rateLimits/read"} {
		if !scanner.Scan() {
			os.Exit(22)
		}
		var request struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Method != method {
			os.Exit(23)
		}
		if method == "initialize" {
			fmt.Println(`{"id":0,"result":{}}`)
		}
	}
	fmt.Println(`{"id":1,"result":{"rateLimits":{"primary":{"usedPercent":23,"windowDurationMins":300}}}}`)
	// A real app-server stays alive until its client disconnects. Collection
	// must finish on the matching response, not on process exit.
	for scanner.Scan() {
	}
	os.Exit(0)
}

func TestCollectCodexCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := collectCodex(ctx, "local", Command{Executable: os.Args[0],
		Arguments:   []string{"-test.run=TestQuotaCLIHelper", "--", "app-server"},
		Environment: append(os.Environ(), "BRIA_QUOTA_HELPER=1", "BRIA_QUOTA_STALL=1")})
	if err != context.DeadlineExceeded {
		t.Fatalf("got %v, want deadline", err)
	}
}

// Opt-in read-only physical acceptance. Never creates a provider conversation.
func TestCollectCodexLive(t *testing.T) {
	executable := os.Getenv("BRIA_QUOTA_LIVE_CODEX")
	if executable == "" {
		t.Skip("set BRIA_QUOTA_LIVE_CODEX for read-only rate-limit acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := collectCodex(ctx, "local", Command{Executable: executable, Arguments: []string{"app-server"},
		Environment: []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FiveHour == nil && snapshot.Weekly == nil {
		t.Fatal("no usage windows")
	}
	if snapshot.FiveHour != nil {
		t.Logf("five-hour used: %d%%", snapshot.FiveHour.UsedPercent)
	}
	if snapshot.Weekly != nil {
		t.Logf("weekly used: %d%%", snapshot.Weekly.UsedPercent)
	}
}

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
