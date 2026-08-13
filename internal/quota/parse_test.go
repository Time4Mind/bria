package quota

import (
	"context"
	"errors"
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
