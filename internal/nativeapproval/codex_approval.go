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

// ErrStale means the authorized picker changed before any key was sent.
var ErrStale = errors.New("command approval changed before input")

// CodexCommandApproval is a display preview, never an executable shell command.
type CodexCommandApproval struct {
	Environment string `json:"environment"`
	Reason      string `json:"reason"`
	Preview     string `json:"preview"`
	Fingerprint string `json:"fingerprint"`
	DecisionID  string `json:"decision_id"`
	OptionCount int    `json:"option_count"`
	Incomplete  bool   `json:"incomplete"`
}

// ParseCodexCommandApproval recognizes the observed Codex command and file-edit menus.
// It does not authorize the action. A caller must independently approve the
// approval policy and bind Terminal to the expected live provider process.
// Wrapped or abbreviated previews must never be reparsed as shell commands.
func ParseCodexCommandApproval(screen string) (CodexCommandApproval, bool) {
	const commandHeading = "Would you like to run the following command?"
	const fileEditHeading = "Would you like to make the following edits?"
	const footer = "Press enter to confirm or esc to cancel"
	var zero CodexCommandApproval
	if len(screen) > 128*1024 {
		return zero, false
	}
	screen = terminalCSI.ReplaceAllString(strings.ReplaceAll(screen, "\r", ""), "")
	lines := strings.Split(strings.TrimSpace(screen), "\n")
	rawLines := append([]string(nil), lines...)
	start := -1
	fileEdit := false
	incompleteFrame := false
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
		if lines[i] == commandHeading || lines[i] == fileEditHeading {
			start = i
			fileEdit = lines[i] == fileEditHeading
		}
	}
	if lines[len(lines)-1] != footer {
		return zero, false
	}
	if start < 0 {
		joined := strings.Join(lines, "\n")
		for _, foreign := range []string{"Would you like", "Select Model", "Choose an approach"} {
			if strings.Contains(joined, foreign) {
				return zero, false
			}
		}
		fileEdit = strings.Contains(joined, "2. Yes, and don't ask again for these files (a)")
		command := strings.Contains(joined, "2. Yes, and don't ask again for commands that start with `")
		if !fileEdit && !command {
			return zero, false
		}
		if fileEdit && !validIncompleteFileEditPrefix(lines) {
			return zero, false
		}
		start = 0
		incompleteFrame = true
	}
	lines = lines[start:]
	rawLines = rawLines[start:]
	request := CodexCommandApproval{}
	if fileEdit {
		request.Environment = "local"
	}
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
		if fileEdit && strings.HasPrefix(line, "Description: ") && request.Reason == "" {
			request.Reason = strings.TrimSpace(strings.TrimPrefix(line, "Description: "))
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
	collapsed := incompleteFrame || NeedsExpansion(strings.Join(lines, "\n"))
	if incompleteFrame && request.Reason == "" {
		request.Reason = "Incomplete Codex approval"
	}
	if selected != 1 || options < 0 || !incompleteFrame && options < 1 || !declined || (request.Environment != "local" && !(collapsed && request.Environment == "")) {
		return zero, false
	}
	if !fileEdit && !collapsed && (request.Reason == "" || command < 0 || options <= command) {
		return zero, false
	}
	if fileEdit && (request.Reason == "" || options <= 1) {
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
	decisionSource := ""
	if len(nonempty) == 1 && nonempty[0] == "2. No, and tell Codex what to do differently (esc)" {
		request.OptionCount = 2
	} else if len(nonempty) >= 2 && nonempty[len(nonempty)-1] == "3. No, and tell Codex what to do differently (esc)" {
		const persistentPrefix = "2. Yes, and don't ask again for commands that start with `"
		const persistentSuffix = "` (p)"
		persistentOption := strings.Join(nonempty[:len(nonempty)-1], " ")
		if fileEdit {
			if persistentOption != "2. Yes, and don't ask again for these files (a)" {
				return zero, false
			}
			// The final destination remains adjacent to the picker when the heading
			// scrolls above the viewport. It is stable across that reflow while
			// keeping unrelated file-edit dialogs distinct in one CLI generation.
			lastDestination, ok := lastFileEditDestination(lines[:options])
			if !ok {
				return zero, false
			}
			decisionSource = "codex:file-edit:" + lastDestination
		} else {
			if !strings.HasPrefix(persistentOption, persistentPrefix) || !strings.HasSuffix(persistentOption, persistentSuffix) {
				return zero, false
			}
			decisionSource = strings.TrimSuffix(strings.TrimPrefix(persistentOption, persistentPrefix), persistentSuffix)
		}
		request.OptionCount = 3
	} else {
		return zero, false
	}
	if command >= 0 && options > command {
		request.Preview = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.Join(rawLines[command:options], "\n")), "$ "))
	} else if fileEdit && options > 1 {
		request.Preview = strings.TrimSpace(strings.Join(rawLines[1:options], "\n"))
		if incompleteFrame {
			request.Preview = strings.TrimSpace(strings.Join(rawLines[:options], "\n"))
		}
	}
	request.Incomplete = collapsed || strings.Contains(request.Preview, "…") || strings.Contains(request.Preview, "[truncated]")
	if request.Preview == "" && !collapsed {
		return zero, false
	}
	if decisionSource == "" {
		decisionSource = request.Environment + "\x00" + request.Reason + "\x00" + request.Preview
	}
	decisionSum := sha256.Sum256([]byte(strings.Join(strings.Fields(decisionSource), " ")))
	request.DecisionID = hex.EncodeToString(decisionSum[:])
	sum := sha256.Sum256([]byte(strings.Join(rawLines, "\n")))
	request.Fingerprint = hex.EncodeToString(sum[:])
	return request, true
}

func validIncompleteFileEditPrefix(lines []string) bool {
	selected := -1
	for i, line := range lines {
		if line == "› 1. Yes, proceed (y)" {
			selected = i
			break
		}
	}
	if selected < 1 {
		return false
	}
	seenDestination := false
	for _, line := range lines[:selected] {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "Description: "):
		case line == "Destination:" || strings.HasPrefix(line, "Destination: "):
			seenDestination = true
		case strings.HasPrefix(line, "/"):
			// Observed wrapped continuation of a Destination field.
		default:
			return false
		}
	}
	return seenDestination
}

func lastFileEditDestination(lines []string) (string, bool) {
	last := ""
	wantsContinuation := false
	for _, line := range lines {
		switch {
		case line == "Destination:":
			wantsContinuation = true
		case strings.HasPrefix(line, "Destination: "):
			last = strings.TrimSpace(strings.TrimPrefix(line, "Destination: "))
			wantsContinuation = false
		case wantsContinuation && strings.HasPrefix(line, "/"):
			last = line
			wantsContinuation = false
		}
	}
	return last, strings.HasPrefix(last, "/")
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
			return ErrStale
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
		active, pending := ParseCodexCommandApproval(screen)
		if approvalReceiptCount(screen) > beforeReceipts && (!pending || active.Fingerprint != fingerprint) {
			return nil
		}
	}
}

func approvalReceiptCount(screen string) int {
	count := 0
	for _, line := range strings.Split(screen, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "✔ You approved codex to ") {
			count++
		}
	}
	return count
}
