package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"bria/internal/authflow"
)

const codexAuthLifetime = 15 * time.Minute

type authRunner interface {
	Status(context.Context) error
	Login(context.Context, []byte) error
}

// CLIAuthenticator delegates credential persistence exclusively to the
// official Codex CLI. Secrets are supplied on stdin and never retained,
// logged, added to argv, or copied into Bria state.
type CLIAuthenticator struct {
	runner authRunner
	now    func() time.Time
}

var _ authflow.Authenticator = (*CLIAuthenticator)(nil)

func NewCLIAuthenticator(path string) (*CLIAuthenticator, error) {
	runner, err := newPinnedAuthRunner(path)
	if err != nil {
		return nil, err
	}
	return &CLIAuthenticator{runner: runner, now: time.Now}, nil
}

func newCLIAuthenticator(runner authRunner, now func() time.Time) (*CLIAuthenticator, error) {
	if runner == nil || now == nil {
		return nil, ErrInvalidConfiguration
	}
	return &CLIAuthenticator{runner: runner, now: now}, nil
}

func (auth *CLIAuthenticator) Begin(ctx context.Context, request authflow.BeginRequest) (authflow.BeginResult, error) {
	if ctx == nil || ctx.Err() != nil || request.Provider != authflow.ProviderCodex || strings.TrimSpace(request.OperationID) == "" || strings.TrimSpace(request.ComputerID) == "" {
		return authflow.BeginResult{}, authflow.ErrAuthorizationUnconfirmed
	}
	return authflow.BeginResult{
		ChallengeReference: codexChallenge(request.OperationID, request.ComputerID),
		Instruction:        "Отправьте API key OpenAI одним сообщением. Bria передаст его официальной команде Codex через stdin и удалит сообщение.",
		ExpiresAt:          auth.now().UTC().Add(codexAuthLifetime),
	}, nil
}

func (auth *CLIAuthenticator) Complete(ctx context.Context, request authflow.CompleteRequest) error {
	if ctx == nil || ctx.Err() != nil || request.Provider != authflow.ProviderCodex || request.ChallengeReference != codexChallenge(request.OperationID, request.ComputerID) {
		return authflow.ErrAuthorizationUnconfirmed
	}
	secret := request.Secret.Bytes()
	defer zeroBytes(secret)
	if len(secret) == 0 || len(secret) > 64<<10 || bytes.IndexByte(secret, 0) >= 0 || bytes.IndexByte(secret, '\n') >= 0 || bytes.IndexByte(secret, '\r') >= 0 {
		return authflow.ErrAuthorizationUnconfirmed
	}
	// An already authenticated official CLI is the durable idempotence fence
	// after a coordinator crash following successful credential persistence.
	if auth.runner.Status(ctx) == nil {
		return nil
	}
	if err := auth.runner.Login(ctx, secret); err != nil {
		return authflow.ErrAuthorizationUnconfirmed
	}
	if err := auth.runner.Status(ctx); err != nil {
		return authflow.ErrAuthorizationUnconfirmed
	}
	return nil
}

func codexChallenge(operationID, computerID string) string {
	sum := sha256.Sum256([]byte(operationID + "\x00" + computerID))
	return "codex-api-key:" + hex.EncodeToString(sum[:16])
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type pinnedAuthRunner struct {
	path     string
	identity os.FileInfo
}

func newPinnedAuthRunner(path string) (*pinnedAuthRunner, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, ErrInvalidConfiguration
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return nil, ErrInvalidConfiguration
	}
	return &pinnedAuthRunner{path: resolved, identity: info}, nil
}

func (runner *pinnedAuthRunner) matches() bool {
	info, err := os.Stat(runner.path)
	return err == nil && info.Mode().IsRegular() && os.SameFile(info, runner.identity)
}

func (runner *pinnedAuthRunner) Status(ctx context.Context) error {
	if !runner.matches() {
		return authflow.ErrAuthorizationUnconfirmed
	}
	command := exec.CommandContext(ctx, runner.path, "login", "status")
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func (runner *pinnedAuthRunner) Login(ctx context.Context, secret []byte) error {
	if !runner.matches() {
		return authflow.ErrAuthorizationUnconfirmed
	}
	input := append(append([]byte(nil), secret...), '\n')
	defer zeroBytes(input)
	command := exec.CommandContext(ctx, runner.path, "login", "--with-api-key")
	command.Stdin = bytes.NewReader(input)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ctx.Err()
		}
		return authflow.ErrAuthorizationUnconfirmed
	}
	return nil
}
