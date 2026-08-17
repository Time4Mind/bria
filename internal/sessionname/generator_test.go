package sessionname

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type runnerStub struct {
	stdout []byte
	args   []string
	env    []string
}

func (r *runnerStub) Run(
	_ context.Context,
	environment []string,
	_ string,
	args ...string,
) ([]byte, []byte, int, error) {
	r.args = append([]string(nil), args...)
	r.env = append([]string(nil), environment...)
	return append([]byte(nil), r.stdout...), nil, 0, nil
}

func TestGeneratorUsesCheapProviderModelsAndSanitizesOutput(t *testing.T) {
	for _, test := range []struct {
		backend string
		model   string
		want    string
	}{
		{backend: "claude", model: "haiku", want: "fix archive"},
		{backend: "codex", model: "gpt-mini", want: "fix archive"},
	} {
		t.Run(test.backend, func(t *testing.T) {
			runner := &runnerStub{stdout: []byte("Fix---Archive---Flow\nextra")}
			generator, err := NewGenerator(runner, map[string]Command{
				test.backend: {Executable: test.backend, Model: test.model},
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			name, err := generator.Generate(context.Background(), test.backend, "restore this session")
			if err != nil || name != test.want {
				t.Fatalf("name=%q err=%v", name, err)
			}
			if !containsPair(runner.args, "--model", test.model) {
				t.Fatalf("args=%v", runner.args)
			}
			if test.backend == "claude" && !containsValue(runner.env, "IS_SANDBOX=1") {
				t.Fatalf("Claude environment=%v", runner.env)
			}
		})
	}
}

func TestGeneratorRejectsRefusals(t *testing.T) {
	runner := &runnerStub{stdout: []byte("I'm sorry, I cannot help")}
	generator, err := NewGenerator(runner, map[string]Command{
		"claude": {Executable: "claude", Model: "haiku"},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Generate(context.Background(), "claude", "sensitive seed"); err == nil {
		t.Fatal("refusal unexpectedly became a session name")
	}
}

func TestGeneratorLimitsNameToTwelveCharacters(t *testing.T) {
	runner := &runnerStub{stdout: []byte("Very-Long-Session-Name")}
	generator, err := NewGenerator(runner, map[string]Command{
		"codex": {Executable: "codex", Model: "gpt-mini"},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	name, err := generator.Generate(context.Background(), "codex", "some prompt")
	if err != nil || len(name) > 12 || len(strings.Fields(name)) > 2 {
		t.Fatalf("name=%q err=%v", name, err)
	}
}

func TestGeneratorTruncatesLongUnicodeSeedOnRuneBoundary(t *testing.T) {
	runner := &runnerStub{stdout: []byte("windows-node")}
	generator, err := NewGenerator(runner, map[string]Command{
		"codex": {Executable: "codex", Model: "gpt-mini"},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	seed := strings.Repeat("длинный русский запрос ", 20)
	truncated := truncateUTF8(strings.Join(strings.Fields(seed), " "), 200)
	if len(truncated) > 200 || !utf8.ValidString(truncated) {
		t.Fatalf("invalid truncated seed: bytes=%d value=%q", len(truncated), truncated)
	}
	if _, err := generator.Generate(context.Background(), "codex", seed); err != nil {
		t.Fatal(err)
	}
	prompt := runner.args[len(runner.args)-1]
	if !utf8.ValidString(prompt) || !strings.Contains(prompt, truncated) {
		t.Fatalf("naming prompt contains invalid UTF-8: %q", prompt)
	}
}

func containsPair(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if reflect.DeepEqual(values[index:index+2], []string{first, second}) {
			return true
		}
	}
	return false
}

func containsValue(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
