package quota

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type CodexCollector struct {
	runner  runtimehost.InputCommandRunner
	command string
	flags   []string
}

func NewCodexCollector(
	runner runtimehost.InputCommandRunner,
	command string,
	flags []string,
) (*CodexCollector, error) {
	if runner == nil || strings.TrimSpace(command) == "" {
		return nil, errors.New("Codex quota runner and command are required")
	}
	return &CodexCollector{runner: runner, command: command, flags: append([]string(nil), flags...)}, nil
}

func (c *CodexCollector) Backend() string { return "codex" }

func (c *CodexCollector) Collect(ctx context.Context, nodeID domain.NodeID) (domain.QuotaSnapshot, error) {
	path, err := c.runner.LookPath(c.command)
	if err != nil {
		return domain.QuotaSnapshot{}, err
	}
	initialize := []byte(
		`{"method":"initialize","id":0,"params":{"clientInfo":{"name":"bria","title":"Bria","version":"1"}}}` + "\n",
	)
	requests := []byte(strings.Join([]string{
		`{"method":"initialized","params":{}}`,
		`{"method":"account/read","id":2,"params":{}}`,
		`{"method":"account/rateLimits/read","id":1,"params":{}}`,
	}, "\n") + "\n")
	// Codex 0.147 changed a bare `app-server` invocation to daemon-oriented
	// startup. Requesting stdio explicitly keeps this collector a bounded,
	// read-only JSON-RPC exchange and remains accepted by older releases.
	args := append(append([]string(nil), c.flags...), "app-server", "--stdio")
	var result runtimehost.CommandResult
	if conversation, ok := c.runner.(runtimehost.JSONRPCCommandRunner); ok {
		result, err = conversation.RunJSONRPC(ctx, initialize, requests, 1, path, args...)
	} else {
		result, err = c.runner.RunInput(ctx, append(initialize, requests...), path, args...)
	}
	if err != nil {
		return domain.QuotaSnapshot{}, err
	}
	if result.ExitCode != 0 {
		return domain.QuotaSnapshot{}, fmt.Errorf("Codex app-server exited with %d", result.ExitCode)
	}
	return parseCodexResponses(result.Stdout, nodeID, time.Now())
}

func parseCodexResponses(data []byte, nodeID domain.NodeID, collectedAt time.Time) (domain.QuotaSnapshot, error) {
	snapshot := domain.QuotaSnapshot{NodeID: nodeID, Backend: "codex", CollectedAt: collectedAt.UTC()}
	found := false
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			continue
		}
		switch response.ID {
		case 1:
			if parseCodexLimits(response.Result, &snapshot) {
				found = true
			}
		case 2:
			parseCodexAccount(response.Result, &snapshot)
		}
	}
	if err := scanner.Err(); err != nil {
		return domain.QuotaSnapshot{}, err
	}
	if !found {
		return domain.QuotaSnapshot{}, errors.New("Codex returned no rate limits")
	}
	return snapshot, snapshot.Validate()
}

func parseCodexLimits(data []byte, snapshot *domain.QuotaSnapshot) bool {
	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		return false
	}
	raw, _ := result["rateLimits"].(map[string]any)
	if byID, ok := result["rateLimitsByLimitId"].(map[string]any); ok {
		if codex, ok := byID["codex"].(map[string]any); ok {
			raw = codex
		}
	}
	for _, key := range []string{"primary", "secondary"} {
		window, ok := codexWindow(raw[key])
		if !ok {
			continue
		}
		duration, _ := raw[key].(map[string]any)["windowDurationMins"].(float64)
		if duration < 24*60 {
			snapshot.FiveHour = &window
		} else {
			snapshot.Weekly = &window
		}
	}
	return snapshot.FiveHour != nil || snapshot.Weekly != nil
}

func codexWindow(value any) (domain.QuotaWindow, bool) {
	raw, ok := value.(map[string]any)
	used, usedOK := raw["usedPercent"].(float64)
	if !ok || !usedOK {
		return domain.QuotaWindow{}, false
	}
	window := domain.QuotaWindow{UsedPercent: min(100, max(0, int(used+0.5)))}
	if reset, ok := raw["resetsAt"].(float64); ok && reset > 0 {
		window.ResetsAt = time.Unix(int64(reset), 0).UTC()
	}
	return window, true
}

func parseCodexAccount(data []byte, snapshot *domain.QuotaSnapshot) {
	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		return
	}
	account, _ := result["account"].(map[string]any)
	for _, key := range []string{"id", "accountId", "email"} {
		if value, ok := account[key].(string); ok && value != "" {
			if snapshot.AccountID == "" {
				snapshot.AccountID = value
			}
			if key == "email" {
				snapshot.AccountLabel = value
			}
		}
	}
}
