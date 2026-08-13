package interactive

import "regexp"

type expressions []*regexp.Regexp

type pattern struct {
	kind    string
	top     expressions
	bottom  expressions
	minGap  int
	exclude expressions
}

func rx(values ...string) expressions {
	result := make(expressions, 0, len(values))
	for _, value := range values {
		result = append(result, regexp.MustCompile(value))
	}
	return result
}

// Order is significant: specific provider prompts precede the deliberately
// broad, bottom-less fallback used for clipped selection menus.
var patterns = []pattern{
	{
		kind:   "hooks_trust",
		top:    rx(`(?i)^\s*Hooks need review\s*$`),
		bottom: rx(`(?i)^\s*Press enter to confirm or esc to go back\s*$`),
		minGap: 2,
	},
	{
		kind: "plan_approval",
		top: rx(
			`^\s*Would you like to proceed\?`,
			`^\s*Claude has written up a plan`,
		),
		bottom: rx(`^\s*ctrl-g to edit in `, `^\s*Esc to (cancel|exit)`),
		minGap: 2,
	},
	{
		kind:   "question",
		top:    rx(`^\s*←\s+[☐✔☒]`),
		minGap: 1,
	},
	{
		kind:   "question",
		top:    rx(`^\s*[☐✔☒]`),
		bottom: rx(`^\s*Enter to select`),
		minGap: 1,
	},
	{
		kind: "permission",
		top: rx(
			`^\s*Do you want to proceed\?`,
			`^\s*Do you want to make this edit`,
			`^\s*Do you want to create \S`,
			`^\s*Do you want to delete \S`,
		),
		bottom: rx(`^\s*Esc to cancel`),
		minGap: 2,
	},
	{
		kind: "codex_approval",
		top: rx(
			`^\s*Would you like to run the following command\?`,
			`^\s*Would you like to run this command\?`,
		),
		bottom: rx(`(?i)^\s*Press enter to confirm or esc to cancel`),
		minGap: 2,
	},
	{
		kind:   "codex_approval",
		top:    rx(`(?i)^\s*3\.\s*No\b.*\(esc\)\s*$`),
		bottom: rx(`(?i)^\s*Press enter to confirm or esc to cancel`),
		minGap: 1,
	},
	{
		kind:   "permission",
		top:    rx(`^\s*[❯›>]\s*1\.\s*Yes`),
		minGap: 2,
	},
	{
		kind:   "question",
		top:    rx(`^\s*❯\s*\d+\.\s+\S`),
		bottom: rx(`^\s*Enter to select`),
		minGap: 1,
	},
	{
		kind:   "question",
		top:    rx(`^\s*❯\s*Submit\b`, `^\s*\d+\.\s*\[[ xX✔✓]\]`),
		bottom: rx(`^\s*Enter to select`),
		minGap: 1,
	},
	{
		kind:   "bash_approval",
		top:    rx(`^\s*Bash command\s*$`, `^\s*This command requires approval`),
		bottom: rx(`^\s*Esc to cancel`),
		minGap: 2,
	},
	{
		kind:   "restore_checkpoint",
		top:    rx(`^\s*Restore the code`),
		bottom: rx(`^\s*Enter to continue`),
		minGap: 2,
	},
	{
		kind:   "resume_summary",
		top:    rx(`^\s*This session is\b.*\bold\b`, `^\s*Resuming the full session`),
		bottom: rx(`^\s*Enter to confirm`, `^\s*Esc to cancel`),
		minGap: 2,
	},
	{
		kind: "settings",
		top:  rx(`^\s*Settings:.*tab to cycle`, `^\s*Select \w`),
		bottom: rx(
			`Esc to cancel`, `Esc to exit`, `Enter to confirm`,
			`(?i)Press enter to confirm or esc to go back`, `^\s*Type to filter`,
		),
		minGap: 2,
	},
	{
		kind: "question",
		top: rx(
			`^\s*❯\s*\d+\.\s+\S`, `^\s*❯\s*Submit\b`,
			`^\s*\d+\.\s*\[[ xX✔✓]\]`,
		),
		minGap: 1,
		exclude: rx(
			`^\s*❯\s*1\.\s*Yes`, `^\s*Do you want to `,
			`^\s*This command requires approval`, `^\s*Bash command\s*$`,
			`^\s*This session is\b.*\bold\b`, `^\s*Resuming the full session`,
			`^\s*Settings:.*tab to cycle`, `^\s*Select \w`,
			`^\s*Restore the code`, `^\s*Would you like to proceed\?`,
			`^\s*Claude has written up a plan`,
		),
	},
}
