# Luna approval probe

Public copy: work-source identities and business values have been redacted.
Original local receipts and fixtures are retained outside Git in the ignored
`.cache/private-approval-evidence` directory. Fixture identifiers are synthetic.

Task contract: 2026-09-08, Europe/Moscow. Source of requirements: Artem's
current request. Read-only Luna CLI in an isolated tmux session must read one
work Tracker issue, two owner YT tables, one internal Wiki page, and one local
project. Record actual approval UI, parsing rules and verified automatic
responses. Local prototype, fixtures, tests and evidence are authorized; no
production restart, release, remote writes or outbound messages.

The integration owner is the current Codex session. Coverage: Luna owns source
reads and reports only; the owner owns this journal, approval parser and tests.
New package nativeapproval is owned by the integration owner: stdlib-only
interactive-menu extraction, command-approval parsing and one-shot terminal
response; public boundaries are InteractiveContent, ParseCodexCommandApproval
and AcceptCodexCommandOnce. nativecli consumes the
parsed event; the opt-in diagnostic consumes both APIs. Package fixtures and
tests verify this boundary separately. No existing package size limit is raised.
One source-reading worker preserves sequential approval observations. Independent
Luna reviewer bria-luna-review-20260908 owns read-only review of the new parser,
probe command and fixtures, with no file writes or external requests; terminal
criterion is a findings report. The integration owner verifies every finding.
Budget: four source scenarios, bounded
YT schema/metadata and at most three non-sensitive rows per table, no full scans.
Stop a scenario after verified source content or a reproduced current blocker.

| ID | Required outcome and acceptance | Status | Evidence / open boundary |
| --- | --- | --- | --- |
| L1 | Restore current project/runtime context | done | HARNESS, STATUS_AND_NEXT, NATIVE_CLI_PILOT; clean starting worktree; CLI 0.153.4; model cache gpt-5.6-luna |
| L2 | Luna reads local project through tmux | done | Session 01a07f71-f720-7971-952f-43b2e0d5c82c; three requested files read through tool |
| L3 | Luna reads one work issue through tmux | done | Live metadata and nonempty description; exact target/response retained locally; result after one-shot Enter |
| L4 | Luna reads two owner YT tables | done | Exists, schema, row_count and 3 bounded rows each verified; paths/values retained only locally |
| L5 | Luna reads internal Wiki content | done | Exact revision and nonempty body verified; metadata-only response followed by explicit content fetch; private identifiers retained locally |
| L6 | Capture actual approvals and parsing contract | done | Seven sanitized fixtures, two command-approval option layouts, multiline reason/compound command and collapsed viewport; LUNA_APPROVAL_CASES.md |
| L7 | Implement and verify scoped automatic approvals | done | Eight live one-shot approvals; nativeapproval ParseScreen integration; nativeapprovalflow plus correlated approve_once, real tmux adapter receipt test; no persistent approval rules |
| L8 | Synthesize evidence and preserve all unverified boundaries | done | All L1-L12 matched to source/fixtures/live receipts/current tests and four built binaries; documentation linked from README; deployment/full release explicitly not claimed |
| L9 | Reproduce oversized approval; automatically accept recognized incomplete command dialogs too | done | Live 100x16 hides command even in scrollback; direct incomplete acceptance without expand, fingerprint 9b7240...; optional expand also tested. Production does not resize between snapshot/key. |
| L10 | Owner can disable automatic confirmations in settings, including already pending/incomplete dialogs | done | UI action roundtrip, real durable settings/controller observer tests full+collapsed; live OFF 3 observations/0 controls then ON 1 Enter/receipt/Tracker result; legacy/reopen, cancellation/serialization and no resize replay regressions pass |
| L11 | Independent agent inspects current CCBot Codex session launch | done | CCBot eedf111, src/ccbot/config.py and tmux_window.py inspected by agent and owner; defaults/config/env/new/resume documented. No bot mutation or credentials read. |
| L12 | Document actual Bria configuration and explicit CCBot-style launch compatibility | done | CONFIGURATION.md and schema-tested docs/examples/bria-single-machine.json; exact CCBot-style mode absent, unsupported hook-bypass flag and strict unknown-field behavior tested; no new mode invented |

Contract amendment from Artem: incomplete command previews are authorized for
automatic one-shot acceptance of a recognized active Codex command approval.
Unknown dialog types, historical screens and ambiguous selected choices remain
non-actionable. This replaces the earlier full-preview requirement for L7/L9;
external source operations still stay within the read-only test contract.

Completed settings implementation worker owned ONLY internal/settings, internal/settingsport,
internal/settingscomposition, internal/telegramsettingsview and their tests.
Public contract: Settings/Effective/settingsport.Snapshot.AutoApproveCommands bool;
JSON auto_approve_commands; optional settingsport.AutoApprovalPreferences with
ToggleAutoApproveCommands(context.Context) error. Default/missing legacy field ON
preserves the requested automatic mode; explicit false stays OFF. CLI settings
category shows the toggle action settings_auto_approve_commands. Main owner owns
controller wiring, launch policy, parser/runtime, repository boundary registration,
documentation and end-to-end checks. No overlap and no commits/deploys.

Main-owned new nativeapprovalflow package coordinates automatic decisions across
native snapshot, live settings gate and correlated native control. It owns only
per-process decision deduplication and toggle/dispatch serialization. It imports
domain, nativeapproval and nativecontrolport; controller supplies settings callbacks.
Acceptance: full/collapsed ON sends exactly one approve_once, OFF sends none,
pending OFF is rechecked, changed provider generation cannot receive the key.

Integration owner also owns nativecontrolport (Request/Snapshot/Controller/
ScreenProvider). sessionruntime keeps aliases, so consumers remain compatible;
the flow depends only on the neutral port, not the supervisor implementation.
Its 60-line boundary and the 150-line flow boundary have separate policy tests.
The settings worker additionally supplied only flow/runtimeprotocol/sessionruntime
test files in an isolated ownership phase, then returned to read-only research.

Verification: package race tests passed for nativeapproval, nativeapprovalflow,
nativecli, runtimeprotocol, sessionruntime, settings, settingscomposition,
telegramsettingsview, telegramui, telegrampipeline, telegramruntimecomposition,
config, diagnostic helper and scripts. Native adapter/terminal race tests passed
through isolated real tmux with TMPDIR=/private/tmp and GORACE=atexit_sleep_ms=0
(disables only the child race runtime exit delay, not race detection). Focused
controller Native/SemanticSettings race tests passed. Four current macOS arm64
binaries built in .cache/approval-build; bria version and helper help executed.
Final checks: make check-policy, make check-format, git diff --check and scoped
go vet passed. Native/Config/Build/Version/CommandSet probes passed for cmd/bria,
runtimefactory and singlemachinecomposition. Documentation JSON is decoded by
a regression test, and documented unsupported CCBot-mode fields/flags are tested.

Global gates remain unaccepted: architecture has the same 13 failing categories
as archived HEAD, including 3 pre-existing imports and 10 pre-existing over-cap
packages. This change adds lines to already over-cap nativeadapter (+7), settings
(+10), settingscomposition (+5), telegramcontroller (+54); no old caps raised.
New packages and sessionruntime stay within their caps. Broad controller -race
also reports existing test-helper races; the voice-envelope race was reproduced
from unmodified HEAD in /private/tmp/bria-approval-baseline.5I2R1x.
These are not claimed fixed and no release/full-product acceptance is claimed.
External Telegram delivery, installation, restart, Linux/WSL matrix and literal
CCBot bypass mode remain outside this task's implemented/deployed scope.

Contract amendment: Artem also requests an agent-owned read-only inspection of
current CCBot launch and documentation of Bria config / explicit CCBot-style
launch. The former settings worker now owns read-only CCBot inspection only,
with no file writes. Main owner owns Bria config inspection and documentation.
Budget: current launch/config/tmux source and tests only; stop at an evidenced
launch mapping, do not inspect credentials or unrelated bot features.

Harness references /root task-ledger which is absent on this macOS machine.
This journal is the durable todo for this request. Existing bria-stabilize and
project backlog remain in their original pointers and are not replaced or closed.
