package providermodels

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"bria/internal/config"
	"bria/internal/domain"
)

func TestCatalogHelper(t *testing.T) {
	mode := os.Getenv("CATALOG_HELPER")
	if mode == "" {
		return
	}
	if mode == "stall" {
		time.Sleep(time.Minute)
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	read := func() map[string]any {
		if !scanner.Scan() {
			os.Exit(2)
		}
		var msg map[string]any
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			os.Exit(3)
		}
		return msg
	}
	if read()["method"] != "initialize" {
		os.Exit(4)
	}
	fmt.Println(`{"id":0,"result":{}}`)
	if read()["method"] != "initialized" {
		os.Exit(5)
	}
	for page := 1; page <= 12; page++ {
		msg := read()
		if msg["method"] != "model/list" || msg["id"] != float64(page) {
			os.Exit(6)
		}
		if mode == "rpc-error" {
			fmt.Printf(`{"id":%d,"error":{"message":"private sentinel"}}`+"\n", page)
			continue
		}
		if page > 1 && msg["params"].(map[string]any)["cursor"] != "next" {
			os.Exit(7)
		}
		next := "next"
		if page == 2 && mode != "loop" {
			next = ""
		}
		fmt.Printf(`{"id":%d,"result":{"data":[{"id":"entry-%d","model":"model-%d","displayName":"Model","isDefault":true,"defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"}]}],"nextCursor":%q}}`+"\n", page, page, page, next)
	}
	os.Exit(0)
}

func helperService(t *testing.T, mode string) *Service {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Service{nodeID: "local", executable: executable, arguments: []string{"-test.run=^TestCatalogHelper$", "--", "app-server"}, environment: append(os.Environ(), "CATALOG_HELPER="+mode), gate: make(chan struct{}, 1)}
}

func TestModelsProtocolPaginationAndCacheIsolation(t *testing.T) {
	s := helperService(t, "pages")
	models, err := s.Models(context.Background(), "local", domain.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "model-1" || !reflect.DeepEqual(models[0].Efforts, []string{"low", "medium"}) || !models[0].Default {
		t.Fatalf("models = %#v", models)
	}
	models[0].Efforts[0] = "mutated"
	s.executable = "not-an-executable"
	cached, err := s.Models(context.Background(), "local", domain.ProviderCodex)
	if err != nil || cached[0].Efforts[0] != "low" {
		t.Fatalf("cache = %#v, %v", cached, err)
	}
	s.expires = time.Time{}
	if _, err := s.Models(context.Background(), "local", domain.ProviderCodex); err == nil {
		t.Fatal("expired cache reused")
	}
}

func TestModelsRejectUnsupportedAndCanceled(t *testing.T) {
	s := helperService(t, "pages")
	for _, target := range []struct {
		node     domain.ComputerID
		provider domain.Provider
	}{{"remote", domain.ProviderCodex}, {"local", domain.ProviderClaude}} {
		if _, err := s.Models(context.Background(), target.node, target.provider); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Models(ctx, "local", domain.ProviderCodex); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCatalogRejectsCyclesAndRPCSecrets(t *testing.T) {
	for _, mode := range []string{"loop", "rpc-error"} {
		t.Run(mode, func(t *testing.T) {
			_, err := helperService(t, mode).Models(context.Background(), "local", domain.ProviderCodex)
			if err == nil || strings.Contains(err.Error(), "private sentinel") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCatalogDeadlineKillsAndReaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := helperService(t, "stall").Models(ctx, "local", domain.ProviderCodex)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("deadline error=%v elapsed=%s", err, time.Since(start))
	}
}

func TestFromConfigSanitizesEnvironmentAndRejectsMissingNode(t *testing.T) {
	c := config.Config{Computer: &config.ComputerConfig{ID: "local"}, TelegramToken: config.TelegramTokenRef{EnvVar: "BOT_SECRET"}}
	s, err := FromConfig(c, []string{"HOME=/safe", "PATH=/bin", "BRIA_SECRET=private", "BOT_SECRET=private"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.environment, []string{"HOME=/safe", "PATH=/bin"}) {
		t.Fatal("unsafe environment")
	}
	if _, err := s.Models(context.Background(), "local", domain.ProviderCodex); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	c.Computer = nil
	if _, err := FromConfig(c, []string{"HOME=/safe", "PATH=/bin"}); err == nil {
		t.Fatal("missing node accepted")
	}
}

func TestParsePageFiltersHiddenAndRejectsMalformed(t *testing.T) {
	models, _, err := parsePage([]byte(`{"data":[{"model":"secret","hidden":true},{"id":"fallback","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"low"}]}]}`))
	if err != nil || len(models) != 1 || models[0].ID != "fallback" || models[0].Name != "fallback" || len(models[0].Efforts) != 1 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	for _, input := range []string{`{}`, `{"data":[{}]}`, `not-json`} {
		if _, _, err := parsePage([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
