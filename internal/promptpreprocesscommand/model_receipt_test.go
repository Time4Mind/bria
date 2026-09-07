package promptpreprocesscommand

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"bria/internal/promptpreprocess"
)

func TestMain(m *testing.M) {
	if os.Getenv("PREPROCESS_MODEL_FIXTURE") != "" {
		model, output := "", ""
		for i := 1; i+1 < len(os.Args); i++ {
			if os.Args[i] == "--model" {
				model = os.Args[i+1]
			}
			if os.Args[i] == "--output-last-message" {
				output = os.Args[i+1]
			}
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		switch os.Getenv("PREPROCESS_MODEL_FIXTURE") {
		case "mismatch":
			model = "different-model"
		case "missing":
			model = ""
		}
		if model != "" {
			fmt.Fprintf(os.Stderr, "OpenAI Codex vfixture\n--------\nworkdir: isolated\nmodel: %s\nprovider: openai\n--------\nuser\nmodel: prompt-spoof\n", model)
		}
		if os.Getenv("PREPROCESS_MODEL_FIXTURE") == "timeout" {
			time.Sleep(10 * time.Second)
		}
		if err := os.WriteFile(output, []byte("clean fixture"), 0600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCodexRewriteRejectsMissingOrMismatchedModelReceipt(t *testing.T) {
	path, identity, err := pinnedExecutable(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	selected := candidate{path: path, identity: identity, model: codexModel}
	for _, mode := range []string{"match", "mismatch", "missing"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			out, err := runCodex(ctx, selected, append(os.Environ(), "PREPROCESS_MODEL_FIXTURE="+mode), promptpreprocess.Request{Instruction: "clean", Text: "synthetic text"})
			if mode == "match" {
				if err != nil || out != "clean fixture" {
					t.Fatal("valid CLI receipt was rejected")
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "different-model") || out != "" {
				t.Fatal("unverified model accepted or diagnostic leaked raw model")
			}
		})
	}
}

func TestCodexReceiptCannotComeFromEchoedPromptOrDuplicateModel(t *testing.T) {
	header := "OpenAI Codex vfixture\n--------\nmodel: expected\n--------\n"
	if !confirmedCodexModel(header+"user\nmodel: other", "expected") {
		t.Fatal("echoed prompt overrode model receipt")
	}
	for _, text := range []string{
		"user\n" + header,
		"OpenAI Codex vfixture\n--------\nmodel: expected\nmodel: expected\n--------\n",
		"OpenAI Codex vfixture\n--------\nmodel: expected\n",
		"model: expected",
	} {
		if confirmedCodexModel(text, "expected") {
			t.Fatal("untrusted or incomplete model receipt accepted")
		}
	}
}

func TestTimedOutCodexPreservesReceiptWithoutClaimingRewriteSuccess(t *testing.T) {
	path, identity, err := pinnedExecutable(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	output, err := runCodex(ctx, candidate{path: path, identity: identity, model: codexModel}, append(os.Environ(), "PREPROCESS_MODEL_FIXTURE=timeout"), promptpreprocess.Request{Instruction: "clean", Text: "fixture"})
	var execution *executionError
	if output != "" || !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &execution) || execution.modelEvidence != "codex_cli_header" {
		t.Fatal("timeout lost model receipt or falsely returned rewritten text")
	}
}
