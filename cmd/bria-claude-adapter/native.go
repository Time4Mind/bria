package main

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bria/internal/domain"
	"bria/internal/nativeadapter"
	"bria/internal/provider/claude"
)

func runNative(ctx context.Context, args []string, input io.ReadCloser, output io.Writer) error {
	workdir, err := os.Getwd()
	if err != nil {
		return errors.New("native working directory unavailable")
	}
	configuration, err := nativeConfiguration(args, workdir, os.Environ())
	if err != nil {
		return err
	}
	return nativeadapter.Run(ctx, input, output, configuration)
}

func nativeConfiguration(args []string, workdir string, parent []string) (nativeadapter.Config, error) {
	if len(args) < 2 || args[0] != "--" {
		return nativeadapter.Config{}, errors.New("native raw command required")
	}
	values := map[string]string{}
	for _, entry := range parent {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	mode, resume := values[startModeEnv], values[providerSessionIDEnv]
	if mode != "new" && mode != "resume" || mode == "new" && resume != "" || mode == "resume" && resume == "" {
		return nativeadapter.Config{}, errors.New("native start identity invalid")
	}
	environment := append([]string(nil), parent...)
	credential := values[claude.CredentialFileEnvironment]
	if credential != "" {
		if !filepath.IsAbs(credential) || strings.ContainsRune(credential, 0) {
			return nativeadapter.Config{}, claude.ErrClaudeCredential
		}
		_, err := os.Lstat(credential)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nativeadapter.Config{}, claude.ErrClaudeCredential
		}
		if err == nil {
			// Reuse exact executable pinning and private credential validation;
			// the native planner remains responsible for actual launch arguments.
			spec, err := claude.BuildCommandSpec(args[1], nil, workdir, rand.Reader)
			if err != nil {
				return nativeadapter.Config{}, errors.New("native Claude executable invalid")
			}
			environment, err = spec.EnvironmentWithStoredAPIKey(parent, credential)
			if err != nil {
				return nativeadapter.Config{}, claude.ErrClaudeCredential
			}
		}
	}
	return nativeadapter.Config{Provider: domain.ProviderClaude, Command: append([]string(nil), args[1:]...), Workdir: workdir, ResumeID: resume, StateDir: values["BRIA_NATIVE_STATE_DIR"], Environment: environment}, nil
}
