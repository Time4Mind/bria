package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"bria/internal/domain"
	"bria/internal/nativeadapter"
	"bria/internal/provider/codex"
	"bria/internal/runtimeprotocol"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if len(os.Args) > 1 && os.Args[1] == "--native" {
			_, _ = fmt.Fprintln(os.Stderr, nativeadapter.StartupFailureMarker(err))
		} else {
			_, _ = fmt.Fprintln(os.Stderr, startupFailureMessage(err))
		}
		os.Exit(1)
	}
}

func startupFailureMessage(err error) string {
	if errors.Is(err, codex.ErrThreadNotFound) {
		return runtimeprotocol.StartupFailureThreadNotFound
	}
	return "bria codex adapter failed"
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "--native" {
		return nativeadapter.Main(ctx, domain.ProviderCodex, args[1:], os.Stdin, os.Stdout)
	}
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
	technical, err := parsePreprocessingMode(os.Getenv)
	if err != nil {
		return err
	}
	configuration := adapterConfiguration(command, workdir, resumeThreadID, technical)
	return codex.RunAdapter(ctx, os.Stdin, os.Stdout, configuration)
}

func adapterConfiguration(command []string, workdir, resumeThreadID string, technical bool) codex.AdapterConfig {
	configuration := codex.AdapterConfig{
		RawCommand:     command,
		Workdir:        workdir,
		ResumeThreadID: resumeThreadID,
		ClientInfo:     codex.ClientInfo{Name: "bria-codex-adapter", Version: "1"},
	}
	if technical {
		configuration.ThreadApprovalPolicy = "never"
		configuration.ThreadSandbox = "read-only"
		configuration.RequireReadOnly = true
		configuration.RejectInteractions = true
	}
	return configuration
}

func parsePreprocessingMode(getenv func(string) string) (bool, error) {
	if getenv == nil {
		return false, errors.New("adapter environment contract is missing")
	}
	switch getenv("BRIA_PREPROCESS_SESSION") {
	case "":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("preprocessing adapter mode is invalid")
	}
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
