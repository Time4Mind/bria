// Package nativeapproval parses observed CLI permissions and answers exact,
// owner-authorized command decisions through an owned terminal boundary.
package nativeapproval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Terminal interface {
	Capture(context.Context) (string, error)
	Key(context.Context, string) error
}

var terminalCSI = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")

// CodexCommandApproval is a display preview, never an executable shell command.
type CodexCommandApproval struct {
	Environment string `json:"environment"`
	Reason      string `json:"reason"`
	Preview     string `json:"preview"`
	Fingerprint string `json:"fingerprint"`
	OptionCount int    `json:"option_count"`
	Incomplete  bool   `json:"incomplete"`
}

// ParseCodexCommandApproval recognizes the observed Codex 0.153.4 command menu.
// It does not authorize the action. A caller must independently approve the
// approval policy and bind Terminal to the expected live provider process.
// Wrapped or abbreviated previews must never be reparsed as shell commands.
func ParseCodexCommandApproval(screen string) (CodexCommandApproval, bool) {
	const heading = "Would you like to run the following command?"
	const footer = "Press enter to confirm or esc to cancel"
	var zero CodexCommandApproval
	if len(screen) > 128*1024 {
		return zero, false
	}
	screen = terminalCSI.ReplaceAllString(strings.ReplaceAll(screen, "\r", ""), "")
	lines := strings.Split(strings.TrimSpace(screen), "\n")
	rawLines := append([]string(nil), lines...)
	start := -1
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
		if lines[i] == heading {
			start = i
		}
	}
	if start < 0 || lines[len(lines)-1] != footer {
		return zero, false
	}
	lines = lines[start:]
	rawLines = rawLines[start:]
	request := CodexCommandApproval{}
	command, options, reasonStart := -1, -1, -1
	selected, declined := 0, false
	for i, line := range lines {
		if strings.HasPrefix(line, "Environment: ") && command < 0 {
			request.Environment = strings.TrimPrefix(line, "Environment: ")
		}
		if strings.HasPrefix(line, "Reason: ") && command < 0 {
			request.Reason = strings.TrimPrefix(line, "Reason: ")
			reasonStart = i
		}
		if strings.HasPrefix(line, "$ ") && command < 0 {
			command = i
		}
		if line == "› 1. Yes, proceed (y)" {
			selected++
			options = i
		} else if strings.HasPrefix(line, "›") || strings.HasPrefix(line, "❯") || strings.HasPrefix(line, ">") {
			return zero, false
		}
		if line == "3. No, and tell Codex what to do differently (esc)" || line == "2. No, and tell Codex what to do differently (esc)" {
			declined = true
		}
	}
	collapsed := NeedsExpansion(strings.Join(lines, "\n"))
	if selected != 1 || options < 1 || !declined || (request.Environment != "local" && !(collapsed && request.Environment == "")) {
		return zero, false
	}
	if !collapsed && (request.Reason == "" || command < 0 || options <= command) {
		return zero, false
	}
	if reasonStart >= 0 && command > reasonStart {
		request.Reason = strings.TrimSpace(strings.TrimPrefix(strings.Join(lines[reasonStart:command], "\n"), "Reason: "))
	}
	// Only the two observed complete option layouts are actionable. Wrapped
	// persistent-rule text is a display field, never an authorization policy.
	optionLines := lines[options+1 : len(lines)-1]
	var nonempty []string
	for _, line := range optionLines {
		if line != "" {
			nonempty = append(nonempty, line)
		}
	}
	if len(nonempty) == 1 && nonempty[0] == "2. No, and tell Codex what to do differently (esc)" {
		request.OptionCount = 2
	} else if len(nonempty) >= 2 && strings.HasPrefix(nonempty[0], "2. Yes, and don't ask again for commands that start with `") &&
		strings.HasSuffix(nonempty[len(nonempty)-2], "` (p)") && nonempty[len(nonempty)-1] == "3. No, and tell Codex what to do differently (esc)" {
		request.OptionCount = 3
	} else {
		return zero, false
	}
	if command >= 0 && options > command {
		request.Preview = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.Join(rawLines[command:options], "\n")), "$ "))
	}
	request.Incomplete = collapsed || strings.Contains(request.Preview, "…") || strings.Contains(request.Preview, "[truncated]")
	if request.Preview == "" && !collapsed {
		return zero, false
	}
	sum := sha256.Sum256([]byte(strings.Join(rawLines, "\n")))
	request.Fingerprint = hex.EncodeToString(sum[:])
	return request, true
}

// AcceptCodexCommandOnce sends one Enter only for an independently authorized
// fingerprint with the one-shot choice already selected. Unknown, stale, changed
// or persistent choices are left to the caller. Success proves a UI receipt,
// not command success; the caller must verify the actual tool result separately.
// The native adapter is a single input owner. Serialize in-process diagnostic
// calls too; the standalone probe additionally claims a durable one-shot key.
var codexApprovalInputMu sync.Mutex

func AcceptCodexCommandOnce(ctx context.Context, terminal Terminal, fingerprint string) error {
	codexApprovalInputMu.Lock()
	defer codexApprovalInputMu.Unlock()
	if terminal == nil || len(fingerprint) != 64 {
		return errors.New("invalid approval authorization")
	}
	beforeReceipts := 0
	for i := 0; i < 2; i++ {
		screen, err := terminal.Capture(ctx)
		if err != nil {
			return err
		}
		request, ok := ParseCodexCommandApproval(screen)
		if !ok || request.Fingerprint != fingerprint {
			return errors.New("no matching command approval")
		}
		beforeReceipts = approvalReceiptCount(screen)
	}
	if err := terminal.Key(ctx, "Enter"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return errors.New("approval dismissal unverified; do not resend")
		case <-time.After(50 * time.Millisecond):
		}
		screen, err := terminal.Capture(ctx)
		if err != nil {
			return err
		}
		_, interactive := InteractiveContent(screen)
		if !interactive && !strings.Contains(screen, "Would you like to run the following command?") && approvalReceiptCount(screen) > beforeReceipts {
			return nil
		}
	}
}

func approvalReceiptCount(screen string) int {
	count := 0
	for _, line := range strings.Split(screen, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "✔ You approved codex to run ") {
			count++
		}
	}
	return count
}
