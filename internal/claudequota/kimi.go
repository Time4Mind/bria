package claudequota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramstatus"
)

type kimiClient struct {
	endpoint, key string
	http          *http.Client
}

func newKimiClient(environment []string) *kimiClient {
	values := map[string]string{}
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	if home, err := os.UserHomeDir(); err == nil {
		data, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		var settings struct {
			Env map[string]string `json:"env"`
		}
		if json.Unmarshal(data, &settings) == nil {
			for key, value := range settings.Env {
				if values[key] == "" {
					values[key] = value
				}
			}
		}
	}
	base, err := url.Parse(strings.TrimSpace(values["ANTHROPIC_BASE_URL"]))
	if err != nil || !strings.EqualFold(base.Scheme, "https") || !strings.EqualFold(strings.TrimSuffix(base.Hostname(), "."), "api.kimi.com") {
		return nil
	}
	base.Path, base.RawPath, base.RawQuery, base.Fragment = "/coding/v1/usages", "", "", ""
	key := strings.TrimSpace(values["ANTHROPIC_API_KEY"])
	if key == "" {
		key = strings.TrimSpace(values["ANTHROPIC_AUTH_TOKEN"])
	}
	if key == "" {
		return &kimiClient{endpoint: base.String()}
	}
	return &kimiClient{endpoint: base.String(), key: key, http: &http.Client{Timeout: 15 * time.Second}}
}

func (client *kimiClient) Collect(ctx context.Context, nodeID domain.ComputerID) (telegramstatus.Snapshot, bool, error) {
	if client == nil {
		return telegramstatus.Snapshot{}, false, nil
	}
	if client.key == "" {
		return telegramstatus.Snapshot{}, true, errors.New("Kimi API key is missing")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint, nil)
	if err != nil {
		return telegramstatus.Snapshot{}, true, err
	}
	request.Header.Set("Authorization", "Bearer "+client.key)
	request.Header.Set("x-api-key", client.key)
	response, err := client.http.Do(request)
	if err != nil {
		return telegramstatus.Snapshot{}, true, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return telegramstatus.Snapshot{}, true, err
	}
	if response.StatusCode != http.StatusOK {
		return telegramstatus.Snapshot{}, true, fmt.Errorf("Kimi usage returned HTTP %d", response.StatusCode)
	}
	var value struct {
		User struct {
			ID string `json:"userId"`
		} `json:"user"`
		Usage  detail `json:"usage"`
		Limits []struct {
			Window struct {
				Duration json.RawMessage `json:"duration"`
				Unit     string          `json:"timeUnit"`
			} `json:"window"`
			Detail detail `json:"detail"`
		} `json:"limits"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&value); err != nil {
		return telegramstatus.Snapshot{}, true, err
	}
	snapshot := telegramstatus.Snapshot{ComputerID: nodeID, Provider: domain.ProviderClaude, CollectedAt: time.Now().UTC()}
	if window, ok := value.Usage.window(); ok {
		snapshot.Weekly = &window
	}
	for _, limit := range value.Limits {
		if fiveHour(limit.Window.Duration, limit.Window.Unit) {
			if window, ok := limit.Detail.window(); ok {
				snapshot.FiveHour = &window
				break
			}
		}
	}
	if snapshot.Weekly == nil && snapshot.FiveHour == nil {
		return telegramstatus.Snapshot{}, true, errors.New("Kimi returned no quota windows")
	}
	return snapshot, true, nil
}

type detail struct {
	Limit     json.RawMessage `json:"limit"`
	Used      json.RawMessage `json:"used"`
	Remaining json.RawMessage `json:"remaining"`
	Reset     string          `json:"resetTime"`
}

func (value detail) window() (telegramstatus.Window, bool) {
	limit, lok := number(value.Limit)
	used, uok := number(value.Used)
	if !uok {
		remaining, rok := number(value.Remaining)
		used, uok = limit-remaining, rok
	}
	if !lok || !uok || limit <= 0 {
		return telegramstatus.Window{}, false
	}
	window := telegramstatus.Window{UsedPercent: max(0, min(100, int((used/limit)*100)))}
	if reset, err := time.Parse(time.RFC3339Nano, value.Reset); err == nil {
		window.ResetsAt = reset.UTC()
	}
	return window, true
}
func number(raw json.RawMessage) (float64, bool) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, false
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		value = unquoted
	}
	number, err := strconv.ParseFloat(value, 64)
	return number, err == nil
}
func fiveHour(raw json.RawMessage, unit string) bool {
	value, ok := number(raw)
	unit = strings.ToUpper(strings.TrimSpace(unit))
	return ok && ((unit == "MINUTE" || unit == "MINUTES" || unit == "TIME_UNIT_MINUTE") && value == 300 || (unit == "HOUR" || unit == "HOURS" || unit == "TIME_UNIT_HOUR") && value == 5 || (unit == "SECOND" || unit == "SECONDS" || unit == "TIME_UNIT_SECOND") && value == 18000)
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
