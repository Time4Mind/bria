package sessiondescription

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/sessionname"
)

type runnerStub struct {
	stdout     []byte
	executable string
	args       []string
}

func (r *runnerStub) Run(
	_ context.Context,
	_ []string,
	executable string,
	args ...string,
) ([]byte, []byte, int, error) {
	r.executable = executable
	r.args = append([]string(nil), args...)
	return append([]byte(nil), r.stdout...), nil, 0, nil
}

func TestGeneratorUsesConfiguredCheapModelAndStrictTwoLineJSON(t *testing.T) {
	runner := &runnerStub{stdout: []byte("```json\n" +
		`{"lines":["Суть:  Разобраться с архивом","Задача: Упростить навигацию！"]}` + "\n```")}
	generator, err := NewGenerator(runner, map[string]sessionname.Command{
		"codex": {Executable: "codex", Model: "gpt-5.6-luna"},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := generator.Generate(context.Background(), "CODEX", []string{
		"первый запрос", "второй запрос", "третий запрос", "четвёртый запрос",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "Разобраться с архивом." ||
		lines[1] != "Упростить навигацию！" {
		t.Fatalf("lines=%q", lines)
	}
	joined := strings.Join(runner.args, " ")
	if runner.executable != "codex" || !strings.Contains(joined, "--model gpt-5.6-luna") ||
		strings.Contains(joined, "четвёртый запрос") {
		t.Fatalf("executable=%q args=%q", runner.executable, runner.args)
	}
}

func TestDecodeDescriptionRejectsTrailingOrOversizedOutput(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"lines":["One.","Two."]}{}`),
		[]byte(`{"lines":["One."]}`),
		[]byte(strings.Repeat("x", maxOutputBytes+1)),
	} {
		if _, err := decodeDescription(raw); err == nil {
			t.Fatalf("accepted invalid output of %d bytes", len(raw))
		}
	}
}

func TestBoundedPromptsUsesFirstThreeWithinTotalBudget(t *testing.T) {
	prompts := boundedPrompts([]string{
		strings.Repeat("a", maxPromptBytes), "second", "third", "fourth",
	})
	if len(prompts) != 1 || len(prompts[0]) != maxPromptBytes {
		t.Fatalf("bounded prompts lengths=%v", promptLengths(prompts))
	}
}

func promptLengths(prompts []string) []int {
	lengths := make([]int, len(prompts))
	for index := range prompts {
		lengths[index] = len(prompts[index])
	}
	return lengths
}
