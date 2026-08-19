package quota

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func TestParseCodexResponsesNormalizesAccountAndWindows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	data := strings.Join([]string{
		`{"id":0,"result":{}}`,
		`{"id":2,"result":{"account":{"id":"acct-1","email":"owner@example.test"}}}`,
		`{"id":1,"result":{"rateLimits":{"primary":{"usedPercent":12.4,"windowDurationMins":300,"resetsAt":1700000300},"secondary":{"usedPercent":63.6,"windowDurationMins":10080,"resetsAt":1700600000}}}}`,
	}, "\n")
	snapshot, err := parseCodexResponses([]byte(data), "node-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AccountID != "acct-1" || snapshot.AccountLabel != "owner@example.test" {
		t.Fatalf("account=%q/%q", snapshot.AccountID, snapshot.AccountLabel)
	}
	if snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 12 ||
		snapshot.Weekly == nil || snapshot.Weekly.UsedPercent != 64 {
		t.Fatalf("windows=%#v/%#v", snapshot.FiveHour, snapshot.Weekly)
	}
}

func TestParseClaudeUsageRequiresActualPercentage(t *testing.T) {
	now := time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC)
	if _, ok := ParseClaudeUsage("Current session\nResets 3 pm", "node-a", now); ok {
		t.Fatal("empty usage section accepted")
	}
	snapshot, ok := ParseClaudeUsage(strings.Join([]string{
		"Current session", "31% used", "Resets 3:30 pm",
		"Current week (all models)", "72% used", "Resets Aug 17 at 11 am",
	}, "\n"), "node-a", now)
	if !ok || snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 31 ||
		snapshot.Weekly == nil || snapshot.Weekly.UsedPercent != 72 {
		t.Fatalf("snapshot=%#v ok=%v", snapshot, ok)
	}
	wantReset := time.Date(2026, 8, 10, 15, 30, 0, 0, time.UTC)
	if !snapshot.FiveHour.ResetsAt.Equal(wantReset) {
		t.Fatalf("reset=%s want=%s", snapshot.FiveHour.ResetsAt, wantReset)
	}
	wantWeekly := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
	if !snapshot.Weekly.ResetsAt.Equal(wantWeekly) {
		t.Fatalf("weekly reset=%s want=%s", snapshot.Weekly.ResetsAt, wantWeekly)
	}
}

func TestParseKimiUsageNormalizesWeeklyAndFiveHourWindows(t *testing.T) {
	now := time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)
	snapshot, err := parseKimiUsage([]byte(`{
		"user":{"userId":"account-1"},
		"usage":{"limit":"100","used":"7","remaining":"93","resetTime":"2026-08-26T08:54:28.285282Z"},
		"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","remaining":"76","resetTime":"2026-08-19T18:54:28.285282Z"}}]
	}`), "node-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Backend != "claude" || snapshot.AccountID != "account-1" {
		t.Fatalf("identity=%q/%q", snapshot.Backend, snapshot.AccountID)
	}
	if snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 24 ||
		snapshot.Weekly == nil || snapshot.Weekly.UsedPercent != 7 {
		t.Fatalf("windows=%#v/%#v", snapshot.FiveHour, snapshot.Weekly)
	}
	wantFiveHourReset := time.Date(2026, 8, 19, 18, 54, 28, 285282000, time.UTC)
	if !snapshot.FiveHour.ResetsAt.Equal(wantFiveHourReset) {
		t.Fatalf("five-hour reset=%s want=%s", snapshot.FiveHour.ResetsAt, wantFiveHourReset)
	}
}

func TestClaudeCollectorUsesKimiUsageAPIWithoutStartingTmux(t *testing.T) {
	const apiKey = "test-kimi-key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/usages" {
			t.Fatalf("request=%s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+apiKey ||
			request.Header.Get("x-api-key") != apiKey {
			t.Fatal("Kimi usage request is missing authentication headers")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"usage":{"limit":"100","used":"7"},
			"limits":[{"window":{"duration":"5","timeUnit":"HOUR"},"detail":{"limit":"100","remaining":"100"}}]
		}`))
	}))
	defer server.Close()

	runner := &inputRunnerStub{}
	collector, err := NewClaudeCollector(runner, "bria", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	collector.kimi = &kimiUsageClient{
		httpClient: server.Client(),
		credentials: func() (kimiUsageCredentials, bool, error) {
			return kimiUsageCredentials{Endpoint: server.URL + "/usages", APIKey: apiKey}, true, nil
		},
	}
	snapshot, err := collector.Collect(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 0 ||
		snapshot.Weekly == nil || snapshot.Weekly.UsedPercent != 7 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if len(runner.args) != 0 || len(runner.input) != 0 {
		t.Fatal("Claude tmux fallback ran for configured Kimi")
	}
}

func TestKimiUsageEndpointOnlyAcceptsOfficialCodingHost(t *testing.T) {
	endpoint, ok := kimiUsageEndpoint("https://api.kimi.com/coding/")
	if !ok || endpoint != "https://api.kimi.com/coding/v1/usages" {
		t.Fatalf("endpoint=%q ok=%v", endpoint, ok)
	}
	for _, value := range []string{
		"https://example.test/coding/", "http://api.kimi.com/coding/", "https://api.kimi.com.evil.test/",
	} {
		if endpoint, ok := kimiUsageEndpoint(value); ok {
			t.Fatalf("unsafe endpoint accepted: %q -> %q", value, endpoint)
		}
	}
}

func TestKimiCredentialsAreDiscoveredFromClaudeSettings(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	settings := `{"env":{"ANTHROPIC_BASE_URL":"https://api.kimi.com/coding/","ANTHROPIC_API_KEY":"secret"}}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, configured, err := kimiCredentialsFromClaude()
	if err != nil {
		t.Fatal(err)
	}
	if !configured || credentials.Endpoint != "https://api.kimi.com/coding/v1/usages" ||
		credentials.APIKey != "secret" {
		t.Fatalf("credentials=%#v configured=%v", credentials, configured)
	}
}

type inputRunnerStub struct {
	input  []byte
	args   []string
	staged bool
	result runtimehost.CommandResult
}

func (r *inputRunnerStub) LookPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("empty command")
	}
	return "/bin/" + name, nil
}

func (*inputRunnerStub) Run(context.Context, string, ...string) (runtimehost.CommandResult, error) {
	return runtimehost.CommandResult{}, nil
}

func (r *inputRunnerStub) RunInput(
	_ context.Context,
	input []byte,
	_ string,
	args ...string,
) (runtimehost.CommandResult, error) {
	r.input = slices.Clone(input)
	r.args = slices.Clone(args)
	return r.result, nil
}

func (r *inputRunnerStub) RunJSONRPC(
	_ context.Context,
	initialize []byte,
	requests []byte,
	_ int,
	_ string,
	args ...string,
) (runtimehost.CommandResult, error) {
	r.staged = true
	r.input = append(slices.Clone(initialize), requests...)
	r.args = slices.Clone(args)
	return r.result, nil
}

func TestCodexCollectorUsesReadOnlyAppServerRequests(t *testing.T) {
	runner := &inputRunnerStub{result: runtimehost.CommandResult{Stdout: []byte(
		`{"id":1,"result":{"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":300}}}}` + "\n",
	)}}
	collector, err := NewCodexCollector(runner, "codex", []string{"--profile", "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Collect(context.Background(), domain.NodeID("node-a")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runner.args, []string{"--profile", "work", "app-server", "--stdio"}) {
		t.Fatalf("args=%q", runner.args)
	}
	if !runner.staged {
		t.Fatal("Codex collector did not wait for initialize before rate-limit requests")
	}
	input := string(runner.input)
	for _, method := range []string{"account/read", "account/rateLimits/read"} {
		if !strings.Contains(input, method) {
			t.Fatalf("input does not contain %q: %s", method, input)
		}
	}
}
