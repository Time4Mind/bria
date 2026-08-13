// Package sessionname generates short display names with an isolated,
// inexpensive one-shot call to the session's provider.
package sessionname

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const promptTemplate = "Generate a 2 word kebab-case name (lowercase, hyphenated, exactly two words) for a coding session about: %s\nReply with only the name, no quotes."

var (
	invalidNameCharacters = regexp.MustCompile(`[^a-z0-9-]+`)
	repeatedHyphens       = regexp.MustCompile(`-+`)
	validName             = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	refusalPrefixes       = []string{
		"i cannot", "i can't", "i can not", "i won't", "i will not",
		"i'm unable", "i am unable", "i'm sorry", "i am sorry",
		"i apologize", "i refuse", "sorry", "unfortunately", "as an ai",
		"as a language model",
	}
)

type Command struct {
	Executable string
	Model      string
}

type Runner interface {
	Run(context.Context, []string, string, ...string) ([]byte, []byte, int, error)
}

type Generator struct {
	runner   Runner
	commands map[string]Command
	timeout  time.Duration
}

func NewGenerator(runner Runner, commands map[string]Command, timeout time.Duration) (*Generator, error) {
	if runner == nil || timeout <= 0 {
		return nil, errors.New("naming runner and positive timeout are required")
	}
	copyCommands := make(map[string]Command, len(commands))
	for backend, command := range commands {
		backend = strings.ToLower(strings.TrimSpace(backend))
		if backend == "" || strings.TrimSpace(command.Executable) == "" ||
			strings.TrimSpace(command.Model) == "" {
			return nil, errors.New("naming backend, executable, and model are required")
		}
		copyCommands[backend] = command
	}
	return &Generator{runner: runner, commands: copyCommands, timeout: timeout}, nil
}

func (g *Generator) Generate(ctx context.Context, backend, seed string) (string, error) {
	backend = strings.ToLower(strings.TrimSpace(backend))
	command, ok := g.commands[backend]
	if !ok {
		return "", errors.New("unsupported naming backend")
	}
	seed = strings.Join(strings.Fields(seed), " ")
	if len(seed) > 200 {
		seed = seed[:200]
	}
	if seed == "" {
		return "", errors.New("naming seed is empty")
	}
	prompt := strings.Replace(promptTemplate, "%s", seed, 1)
	args, environment := namingCommand(backend, command.Model, prompt)
	runCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	stdout, _, exitCode, err := g.runner.Run(runCtx, environment, command.Executable, args...)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return "", errors.New("naming command failed")
	}
	return sanitize(stdout)
}

func namingCommand(backend, model, prompt string) ([]string, []string) {
	if backend == "codex" {
		return []string{
			"exec", "--model", model, "-c", `model_reasoning_effort="low"`,
			"--ephemeral", "--ignore-user-config", "--ignore-rules",
			"--sandbox", "read-only", "--skip-git-repo-check", prompt,
		}, os.Environ()
	}
	return []string{"--model", model, "--print", prompt}, claudeEnvironment()
}

func claudeEnvironment() []string {
	blocked := map[string]bool{
		"CLAUDE_CODE_SESSION_ID": true, "CLAUDE_CODE_ENTRYPOINT": true,
		"CLAUDE_CODE_EXECPATH": true, "CLAUDECODE": true, "AI_AGENT": true,
	}
	environment := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] && key != "IS_SANDBOX" {
			environment = append(environment, item)
		}
	}
	return append(environment, "IS_SANDBOX=1")
}

func sanitize(raw []byte) (string, error) {
	text := strings.Trim(strings.TrimSpace(string(raw)), "'\"`")
	lower := strings.ToLower(text)
	for _, prefix := range refusalPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return "", errors.New("naming response was a refusal")
		}
	}
	line, _, _ := strings.Cut(lower, "\n")
	line = invalidNameCharacters.ReplaceAllString(line, "-")
	line = strings.Trim(repeatedHyphens.ReplaceAllString(line, "-"), "-")
	words := strings.Split(line, "-")
	if len(words) > 2 {
		line = strings.Join(words[:2], "-")
	}
	if !validName.MatchString(line) {
		return "", errors.New("naming response is invalid")
	}
	return strings.ReplaceAll(line, "-", " "), nil
}

type ExecRunner struct{}

func (ExecRunner) Run(
	ctx context.Context,
	environment []string,
	name string,
	args ...string,
) ([]byte, []byte, int, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = environment
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0, nil
	}
	if ctx.Err() != nil {
		return stdout.Bytes(), stderr.Bytes(), 0, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return stdout.Bytes(), stderr.Bytes(), exitError.ExitCode(), nil
	}
	return stdout.Bytes(), stderr.Bytes(), 0, err
}
