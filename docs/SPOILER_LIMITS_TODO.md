# A26: Separate command and output spoiler limits

## Current amendment A27: accepted requests retain acceptance

Artem added before push/install: once accepted by the model, a request must not
transition to Unknown; this breaks unarchive and restart recovery. This expands
the same release, not a separate optional backlog. Preserve proven acceptance
durably across shutdown, uncertain process outcome and reconciliation. Without
terminal proof keep accepted/recovery-pending, do not replay the model request,
fake completion, or admit conflicting work. Unknown before proven acceptance
remains distinct. Existing unknown records must only regain acceptance when
existing trustworthy acceptance evidence supports it; do not relabel ambiguity.
Acceptance: public RED/GREEN for accepted -> process loss/restart/unarchive,
reconciliation later completing and releasing queue once, main/steer safety,
exact binding/stale/unknown-before-accept controls, real disk reopen and fullgate.
All A26 and automatic release obligations remain. No external writes until the
whole amended request is ready. Prior fullgate now covers A26 only, not A27.

Initial coverage (read-only diagnosis before assigning changes): Anscombe owns
messagejournal/durableflow state-transition diagnosis; Peirce owns recovery and
unarchive flow diagnosis; Ohm owns controller/turnprocessing completion and main+
steer bridge diagnosis. Main maps exact changes and assigns nonoverlapping
implementation after their bounded findings. Darwin keeps policy ownership;
Bernoulli keeps service preflight; Carver independent final review. Same immutable
amendment for all agents. Initial stop: cause + smallest safe implementation and
public regression seam, no speculative refactor/new general recovery machinery.

| ID | Acceptance | Status | Evidence |
|---|---|---|---|
| A27.1 | Proven acceptance never becomes input Unknown | done | Journal no-downgrade, observed ACK retained, exact write retry, public RED/GREEN |
| A27.2 | Restart/unarchive reconcile retained acceptance and complete once | done | Manager and guarded archive resume reconcile first; live fence preserves failed ACK; late exact final releases B once |
| A27.3 | Regression/reopen/main+steer safety and full amended release | done locally | Final full gate PASS; actual push/install tracked separately in A26.R2/R3 |

Integration evidence 2026-09-08 21:xx UTC: combined race passed acceptedinput,
acceptedrecovery, durableflow, messagejournal, durablecomposition,
durableinputbridge, turnprocessing, telegramcontroller, sessionsupervisor and
singlemachinecomposition. Later independent review found and drove additional
RED/GREEN: Manager Ready/targetReady routing, retain even empty-card sessions on
unproven reconciliation, same-Flow observed ACK fence before lease and before
custody lookup. Plain/race/vet in journal/flow/acceptedinput passed after that;
architecture/format passed without raising any existing package cap.
Cmd permanent journal-ACK-fault integration then reproduced a missing prompt
anchor on exact late-final restore; Ohm owns that final controller correction.
This receipt is intermediate, not the final gate or a deployment claim.

Late-final correction: exact main/steer ACK persists its prompt anchor even if
the journal acceptance commit fails, retaining the original failure. No
final-validation relaxation or synthetic production anchor. Main independently
observed cmd permanent-ACK fault RED `recovered final prompt anchor unavailable`
then GREEN with `go test -race ./cmd/bria -run
TestModelACKSingleCommitFailureRetainsMainAndSteerOnDisk -count=1` (1.370s).
Owners reported full scoped controller plain/race and Manager/supervisor race
PASS. Review required callback/fence before prompt I/O, then unconditional anchor
publication and return of the captured callback error, so cancellation during
prompt persistence cannot lose observed acceptance. Ohm owns this ordering test;
the concurrently running gate is superseded, final gate must use frozen ordering.
Final ownership handoff: main takes controller.go ordering after stopping Ohm;
Darwin owns one new cmd cancellation/blocked-anchor regression, Carver reviews.
Other production/test owners remain frozen. The first amended full gate passed,
but is superseded by this final ordering correction and is not release evidence.
Main independently restored unsafe prompt-before-callback ordering for the new
cmd regression: RED for both main and steer (Pending at blocked prompt write).
Restored reviewed controller SHA256 `67017f2206c7cd2e5b94faf1a766e6e6965587cc3474965d5b2d99e81bbf5101`;
same regression `go test -race ./cmd/bria -run
^TestAcceptedProcessCancellationDuringAnchorWriteRetainsExactCustody$ -count=3`
GREEN 1.258s. Cancellation, real journal reopen and final Close retain Accepted.
Independent static review now has no open required findings; final full gate next.

## Current contract amendment: automatic release

A27 implementation ownership after diagnosis: Anscombe - messagejournal and
durableflow; Ohm - telegramcontroller, turnprocessing, durableinputbridge;
Peirce - durablecomposition, sessionsupervisor, recoverycomposition,
telegramrecoverycomposition; Darwin - cmd/bria acceptance/recovery tests only
after freezing policy; main - docs, any required cmd production wiring and final
integration; Carver - independent review; Bernoulli - read-only service preflight.
Do not change global Accepted-skipping lease behavior required by live steers.
Root-only optional custody guard runs after live-steer branch. Uncertain input
remains accepted; AwaitingRecovery is an observation, not claimed terminal proof.
Architecture-preserving split: Peirce owns new acceptedrecovery (exact accepted
history reconciliation/final restore/archive guard); Anscombe owns new
acceptedinput (exact input/read/root-admission and acceptance custody helpers).
Existing public aliases remain, no old package size limits raised. Main owns
machine registry changes and singlemachinecomposition wiring; app stays unchanged.
Review follow-up ownership: Peirce also owns supervisioncomposition Manager
startup routing and its public regression. Anscombe fences an observed but
uncommitted ACK before a later same-process lease/dispatch; no global change to
normal lease expiry or live steering. Darwin owns cmd-level permanent ACK fault
through expiry/wake/reopen/Manager startup and exact late-final continuation.

Artem's subsequent explicit request on 2026-09-08 authorizes automatic push and
restart after completed development, without another request. This supersedes
the original local-only exclusion below for A26 release and establishes a
standing project workflow for subsequent explicitly requested development.
Targets: Time4Mind/bria origin/main (normal push, never force); local installed
Bria gui/501/com.time4mind.bria.v2, unchanged plist/config/state locations;
release /Users/a-s-nosko/.local/opt/bria-v2/releases/20260908-separate-spoiler-limits
and its three verified binaries, atomic current symlink switch. No other host,
service, secrets edit, outgoing test message, data deletion or manual history edit.

Bounded sequence: update AGENTS and its policy tests to encode the explicit
standing authorization with narrow exclusions; full make check-full; inspect
and commit only A26 + A27 + policy/docs manifest; normal push; verify both current-SHA
GitHub CI runs (in-scope CI-fix iterations included); install exact verified trio,
restart only Bria; reread remote HEAD, version/hash, process/lock, settings and
session/history/journal preservation, safe fresh logs and getMe readiness.
Terminal criterion: current main CI green and installed verified release healthy
with preserved user data. No live visual Telegram claim without observation.
Failure requires a concrete preserved blocker, not automatic unrelated changes.

Release coverage: main owns synthesis/docs, serial Git/deploy and full gate;
Darwin owns AGENTS.md plus scripts policy implementation/tests (TDD);
Bernoulli read-only live preflight and preservation/postflight inspection;
Carver read-only independent review of policy/release scope and final diff.
Prior feature owners are frozen. No useful further independent write zones.

| ID | Acceptance | Status | Evidence |
|---|---|---|---|
| A26.R1 | Standing automatic release rule with protected policy tests | done | Public policy RED/GREEN; machine negative controls and independent review |
| A26.R2 | Full gate, exact commit pushed, current-SHA CI green | pending | Prior scoped checks not full release gate |
| A26.R3 | Exact release installed, Bria restarted, data/health verified | pending | Live preflight current=final-save-retry PID89675 |

## Final amended release gate

2026-09-08 21:38 UTC: main observed final `env TMPDIR=/private/tmp
GOMODCACHE=/Users/a-s-nosko/go/pkg/mod VERSION=20260908-separate-spoiler-limits
make check-full` exit 0. Cmd plain 31.369s/race 52.313s; controller plain
6.413s/race 7.870s. Policy, links, secrets scan, format, all supported-platform
architecture, complete plain/race suites, vet, packaging contracts and physical
trio acceptance PASS. Independent review approve with all required findings closed.
Post-gate `check-config` PASS. Physical Mach-O arm64 trio, version
`20260908-separate-spoiler-limits`, final SHA256 (supersedes A26-only hashes below):

- bria: `f3668a122b5299028f7bc92685ceaec24c9d38002635dc64a0cd2b8f797d07f9`
- bria-codex-adapter: `792059a14d750aae8367d7b23d77434e0bc3cd604d01e7040e1f9800d4f41911`
- bria-claude-adapter: `88d612486c8c3171489dd369673a2e93e05f6b84aa97092bc2403af850648263`

Fresh live preflight 21:36:44 UTC: 5 sessions (3 ready/2 archived), 5 cards,
146 history entries, 16 completed inputs; no active work. Earlier 135/15 snapshot
is stale, not a preservation baseline. Final pre/post-stop checks remain required.
No manual Telegram visual scenario, outgoing test message or live disk fault.

## Immutable task contract

Source: Artem's 2026-09-08 request in this conversation. Implement independent
command and output line settings, each selectable as 3, 5, 10, 20. Keep 100
Unicode characters per wrapped line. Bound command and result separately before
joining; separator and truncation notices do not consume either content budget.
Persist both choices and apply through both transcript projection paths without
changing stored history or re-running tools. Default remains 10 for each. Old
documents inherit the existing shared choice for both; old 40 migrates to 20.
Invalid new values remain rejected. Scope is this repo, code/tests/docs only;
no production state, remote Git, deploy/restart or outgoing messages. Timezone
Europe/Moscow; no time-dependent data population. Acceptance: public RED/GREEN,
real settings persistence/reopen, callback-to-projection integration, relevant
race/architecture/build checks and independent review. Live Telegram unverified.

## Coverage and ownership

- Main: integration owner; docs, telegramsettings, telegramcontroller settings dispatch/projection
  and tests; connect independent settings/render/UI boundaries.
- Darwin: internal/settings, settingscodec, settingscomposition, settingsport,
  settingscapability only; persistence/migration and independent cycle methods.
- Anscombe: internal/cardtranscript and internal/tooltext only; independent
  truncation and Unicode/paired metadata regression tests.
- Bernoulli: callbacktoken, telegramsemantic, telegramui, telegramsettingsview,
  telegramcallbackview, telegramruntimecomposition, telegrampipeline only;
  new command-lines action through callback routing, preserve existing IDs.
- Peirce: internal/telegramflow tests only; actual callback/settings/render
  integration and compatibility regression assertions.
- Carver: read-only independent review after integration.
- Ohm: cmd/bria/tool_retention_integration_test.go only; native protocol through
  real persisted history and Rich wire with independent content budgets.

Public boundary: TechnicalCommandLines added alongside TechnicalOutputLines;
CycleTechnicalCommandLines alongside CycleTechnicalOutputLines; action
settings_technical_command_lines. cardtranscript.Block adds CommandLines while
ToolLines remains the output limit. Each zone stops at focused tests passing;
no external systems. One relevant combined gate after integration, full release
gate only if a release is requested. Shared files never have two writers.

Read-only diagnosis also found shared retention in tooltext.Encode before the
renderer. New events must retain at least 20 lines of each field independently
within the existing 16 KiB history record bound; old records are not rewritten.

## Obligations

| ID | Acceptance | Status | Evidence / unverified boundary |
|---|---|---|---|
| A26.1 | Two independent persisted settings, 3/5/10/20, old-file migration | done | Public defaults/migration RED -> GREEN; v1-v5 reopen, independent cycles, invalid new values and concurrent updates |
| A26.2 | Command and output independently bounded, 100 Unicode chars/line | done | Shared-budget disappearance and paired 20+20 Encode RED -> GREEN; Unicode/escaping, truncation, 16 KiB retention |
| A26.3 | Both UI controls, callback routing and both projection paths | done | Signed callback/reopen/Rich wire 3+3, 3+5, 5+3; auth/stale/replay, current/completion with persisted and memory history |
| A26.4 | Fresh tests/race/architecture/build and independent review | done | Combined relevant plain/race PASS, policy/format/architecture/vet/build PASS, independent final review approve; live UI/deploy excluded |

## Acceptance receipt

Main independently observed original public controller failure: output limit 3
rendered ROW01..ROW10 on both persisted/memory and current/completion paths.
Independent command action initially failed with missing session ID instead of
the persistence error. After renderer wiring, command limit 3/5 still rendered
CMD01..CMD10; applying TechnicalCommandLines to both projection paths fixed it.
Final tests assert both content budgets and unchanged persisted blocks.

Fresh combined `go test -race -mod=readonly ... -count=1` PASS across settings,
settingscodec, settingscomposition, cardtranscript, tooltext, callbacktoken,
telegramsettings, telegramsettingsview, telegramcallbackview, telegramcontroller,
telegramflow, telegrampipeline, telegramruntimecomposition and telegramui.
Controller 7.464s, flow 10.589s. Offline environment: TMPDIR=/private/tmp,
GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off,
GOCACHE=/Users/a-s-nosko/pet_projects/bria/.cache/go-build,
GOMODCACHE=/Users/a-s-nosko/go/pkg/mod; race CGO_ENABLED=1,
GORACE=atexit_sleep_ms=0, plain CGO_ENABLED=0.

Final `go test -race -mod=readonly ./cmd/bria -run 'TestA2[56]ToolRetention'
-count=1` PASS 5.966s: native parsing
through protocol and on-disk history to real Rich sender with local fake HTTP.
Four original output-only cases plus eight independent emoji/control
2000/2001/4000/4001 cases, 20+20/3+5/5+3 then expanded 20+20,
exact text across continuation pages; no history rewrite or native event replay.

`env TMPDIR=/private/tmp GOMODCACHE=/Users/a-s-nosko/go/pkg/mod
VERSION=20260908-separate-spoiler-limits make check-policy check-format
check-architecture check-vet build` PASS. No architecture budgets raised.
Physical local trio: Mach-O arm64; `bin/bria --version` returns
`bria 20260908-separate-spoiler-limits`. SHA256:

- bria: `6f4d3337a77f01c2d96281237293e8888d7af08bcd0f88272f87f22d04ea6b17`
- bria-codex-adapter: `421ec5907c510482e67af10b3ee77dc6eb4a175012d8f5ce0ea21e4d03c4282c`
- bria-claude-adapter: `5b8ffacb1fb002b520c43a41ea0234b0f8e0a23d2448ae5df855f14b596e619e`

Independent review against b767f9e found no required production corrections;
its remaining cmd race/build checks completed above. Documentation clarified
retention: preserve up to 40 total when possible; otherwise reserve 20 each.
Final reviewer verdict: approve. Final fresh combined plain suite also PASS
(controller 6.493s, flow 9.392s); post-documentation policy/format PASS.
Local implementation only: no commit/push/CI/install/restart/live Telegram.
Old output already truncated by an older encoder cannot be recovered by settings.
