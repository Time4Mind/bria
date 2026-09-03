package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"bria/internal/provider/codex"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "bria codex adapter failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	command, err := parseRawCommand(args)
	if err != nil {
		return err
	}
	workdir, err := os.Getwd()
	if err != nil {
		return errors.New("cannot determine adapter workdir")
	}
	resumeThreadID, err := parseAdapterStart(os.Getenv)
	if err != nil {
		return err
	}
	return codex.RunAdapter(ctx, os.Stdin, os.Stdout, codex.AdapterConfig{
		RawCommand:     command,
		Workdir:        workdir,
		ResumeThreadID: resumeThreadID,
		ClientInfo:     codex.ClientInfo{Name: "bria-codex-adapter", Version: "1"},
	})
}

func parseRawCommand(args []string) ([]string, error) {
	if len(args) < 2 || args[0] != "--" {
		return nil, errors.New("raw codex app-server argv must follow --")
	}
	return append([]string(nil), args[1:]...), nil
}

func parseAdapterStart(getenv func(string) string) (string, error) {
	if getenv == nil {
		return "", errors.New("adapter start contract is missing")
	}
	mode := getenv("BRIA_START_MODE")
	providerSessionID := getenv("BRIA_PROVIDER_SESSION_ID")
	switch mode {
	case "new":
		if providerSessionID != "" {
			return "", errors.New("new adapter start must not include a provider session")
		}
		return "", nil
	case "resume":
		if !validProviderSessionID(providerSessionID) {
			return "", errors.New("resume adapter start requires a valid provider session")
		}
		return providerSessionID, nil
	default:
		return "", errors.New("adapter start mode must be explicit")
	}
}

func validProviderSessionID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
