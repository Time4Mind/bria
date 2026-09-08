# Architecture and race fixes - 2026-09-08

## Immutable task contract

Owner: main agent. Source: current repository and Artem's requests to fix the
reported architecture/race defects, then push Git and restart the service.
Scope: three reproduced controller test race groups, three forbidden dependency
edges, ten oversized packages. Preserve current approval/configuration changes,
early durable acceptance semantics, package budgets and runtime behavior.
No legacy repository comparison. Timezone: Europe/Moscow; no data population.
Unit of acceptance: each defect plus integrated consumer regression checks.
Local source/test/docs/build writes authorized. Push/restart remain gated on
exact targets and the project's bounded-sequence confirmation rules. Never
touch unrelated bots, credentials or user changes.

## Coverage and ownership

## A25 - agreed large technical events and spoiler limits (2026-09-08)

### Current agreement revision 7 - automatic final-save retry accepted

Artem explicitly selected automatic retry and requested implementation. Main owns
the whole remaining request, including previously conditional Git push and restart
after full readiness. Result: retry only the already received exact final write;
never replay provider input, restart provider, delete history or add a manual button.
Source current repository plus the user's automatic choice; Europe/Moscow for
evidence; unit exact session/provider binding/message/final and its next queued input.
First failed save keeps finalization pending and new-root admission blocked.
Retry pauses 1,2,4,8,16,30 seconds then at most once per30seconds until save succeeds,
controller shuts down or exact binding becomes stale. First attempt preserves the
existing bounded shutdown drain; no further retry after shutdown. Successful save
continues existing final-before-Finish and main/steer commit admission exactly once.
Readable first failure/recovery notices and payload-free recurring diagnostics;
no sensitive error/prompt text in logs. Preserve current A17-A25 work and state.

Acceptance: public RED->GREEN transient failure, uncertain-success/idempotence,
persistent failure/no next send, cancellation, exact binding supersession,
actual runtime/store/journal/queue and main+steer consumer tests; independent review,
full gate/physical build. Local source/tests/docs/build authorized. Main alone owns
eventual exact-target release/preflight/postflight; no agent external writes.
Current local Darwin/arm64 build path applies; do not infer permission for the
unrelated remote Linux build-host paragraph in HARNESS.

Coverage before fanout: main owns controller production/new controller tests and
registry/docs, integrating and releasing serially. Darwin owns new finalpersist
retry helper and its public tests only (120-line initial budget, no internal imports).
Peirce owns new cmd/bria/final_save_retry_test.go plus adaptation of previous
accepted_terminal_journal_test.go storage-failure expectations only. Ohm owns
controllertelemetry final-save stage and observability tests only; proposes exact
safe diagnostic mapping, no controller edits. Bernoulli does read-only installed
service/release target preflight; no writes or sensitive values. Carver reviews
finalization/shutdown/generation safety after frozen implementation. Useful disjoint
zones only, one main integration owner; one final full gate after scoped tests.
Guard refinement before dependent code: main also owns finalpersist/save.go and
its tests (combined retry/guard package budget200); Darwin still owns only retry
files. Anscombe owns storage/bound_final.go and bound_final_test.go: exact current
session equality and final write under the same existing state mutex/transaction.
Optional bound writer API returns false/nil for stale state; no overwrite/retry
against a replacement binding. Fault-injection fixtures must override this method
as well, not accidentally bypass errors through an embedded production store.
Storage budget refinement: main mechanically extracts canonical duplicate-JSON-key
validation from session_store.go into statejson (100-line budget, unchanged
behavior and characterization tests), freeing space for the atomic bound writer.
Main owns that source extraction/registry; Anscombe still solely owns new bound
writer files. No existing architecture cap is raised; no unrelated parser adoption.
Main also owns safe finalpersist failure classification/cardhistory sentinels;
only closed reason strings reach UI. Carver temporarily owns technical_history.go
and final_retry_notification_test.go for the review-found cancellation regression:
retry notices must use cancellable root context, while diagnostic logging remains.
No other ownership or requested behavior changes.
Carver's two public notification-cancellation regressions are RED->GREEN and
race x3 PASS; both notices now retain root cancellation. Ohm then found that
closeFlow.Observe replaces the event operation from context, losing a rootless
final-save correlation. Ohm temporarily owns technical_history.go and new
final_retry_log_test.go only: bind the exact message-derived operation context and
parent operation, prove controller -> physical JSONL, preserve cancellable notices.
Main will rerun the final full gate after this diagnostic refinement freezes.
The post-correlation gate correctly rejected concrete log-adapter imports in the
controller test package. Ohm owns relocation of that regression to the integration
composition boundary only; production remains frozen and architecture policy is
not relaxed. The rejected gate is not acceptance evidence.

| ID | Obligation | Status / acceptance |
| --- | --- | --- |
| A25.F1.1 | paced cancellable final-write retry, exact guard and idempotence | locally verified; public transient/ambiguous/persistent/shutdown/stale tests and atomic physical store tests PASS |
| A25.F1.2 | main/steer journal and next-input/card continuity | locally verified; actual runtime/store/main+steer/B-once race PASS 13.843s, independent repeat x3 PASS 42.140s |
| A25.F1.3 | safe error/recovery diagnostics and documentation | locally verified; controller to physical JSONL exact operation/parent refs and private sentinel absence, cancellable notices; main fresh scoped race PASS 5.347s |
| A25.F1.4 | complete local gate/build/review | complete: final make check-full PASS 18:40 UTC; independent helper/storage and notification/correlation approvals; physical trio verified below |
| A25.R3 | conditional push/restart after full completion | complete; source00b26a7 remote verified, both CI workflows PASS, installed exact trio and restarted service; live process/lock/identity/recovery/data postflight below |

Release preflight (2026-09-08 18:34 UTC): origin Time4Mind/bria main remains
24921b7ebcab9b23a16683253f9a4461c84b01ac. Exact source manifest has 268 paths
(cmd20/docs7/internal239/scripts2), SHA256
dcf128cd175ff48b9a53dd2f29055623cc70af9b9a8ba1befb4d2adbb71e741f.
No secrets/runtime files/binaries included. Four removed source files are covered
by the prior format/notificationstate extraction, not user-data deletion.
New code validates isolated copies of installed state/journal: 5 sessions
(archived2/awaiting_recovery1/ready2), 10 journal buckets/15 completed inputs.
Live OpenSessionStore was not used successfully: its existing chmod side effect
is blocked in the sandbox; validation was rerun on isolated copies instead.
Configuration check PASS. Installed service gui/501/com.time4mind.bria.v2 remains
PID89657/current20260908-card-order; new release path does not yet exist.
Baseline 18:19:02-18:34:02 UTC has no fresh events in the three safe log files;
this is not proof of health. Existing awaiting_recovery remains a live boundary.
Release will install exactly three Darwin/arm64 binaries under
~/.local/opt/bria-v2/releases/20260908-final-save-retry and change current only;
no manual config/state/journal mutation, new backup, dependency installer or
unrelated service action. Old binary-only rollback is not safe for the new formats.
The existing settings snapshot also validates using the new reader. Installed
notification-parts file is absent; the corresponding empty-store preflight passes
on the isolated path (no historical parts compatibility claim from a missing file).

Final revision7 local acceptance, 2026-09-08 18:40 UTC: canonical `make check-full`
PASS (policy, secret scan, formatting, all registered platform graphs, tests,
vet, operational contracts, all-package race, executable trio). Actual cmd/bria
suite plain26.904s/race43.853s; main fresh relocated log regression race2.393s.
Storage/helper review Bernoulli approve; cancellation/correlation review Darwin
approve. Physical final-save persistence is proved by the actual runtime/storage
tests, while the separate physical JSONL test uses a synthetic fault writer.
Binary version20260908-final-save-retry, all Mach-O arm64; SHA256:

- bria: 67740de166d39a1200bcae5c2175ed2c7c3e0a00747c3c1c25b931890dd154a7
- bria-codex-adapter: 9ac46e30e6ffe3f21f89a3467bb6aed5cb540af22b0c8fcb8e28d631f1fa38f9
- bria-claude-adapter: 0964ef3d792242e2bc3fcf3f606412cdb70b3d8c39074a379dc8061694159e53

All agreed local A17-A25 obligations are complete; historical not-authorized or
F1-unimplemented rows are superseded by revision7 and current conditional release.
Remaining current obligation A25.R3: publish exact manifest, verify remote/CI,
install/restart exact service and postflight. Manual Telegram UI scenario and live
fault injection are not claimed by local tests or readiness checks.

Release execution receipt (in progress): source commit
4d82381d8ceb940017c974896cf0c8c8f4dfc107 pushed normally to origin/main;
independent ls-remote equals that exact commit. Git tree contains AGENTS.md,
staged paths matched the manifest exactly (Git rename detection reports266 files
for268 raw paths). No source changes after local gate other than task records.
CI runs: Stage1 34264518186 and Platform matrix34264518153, currently running.
Read-only Telegram getMe with the new binary confirms expected bot identity.
Before deployment lock-file owner and launchd PID both89657; the current agent
process tree is a separate tmux descendant, not a Bria descendant.

CI follow-up within the same bounded release: macOS native job102190422730
(run34264518153) fails TestA25NativeExitDiagnosticReachesPhysicalSafeLogWithExactCorrelation
at line86: accepted identity/final absence/safe class are correct, missing model
event is the discriminating unchecked field. Linux native/cross builds/Docker pass.
Install remains unperformed. Hypotheses: (1) process-done select wins over already
buffered stdout, (2) reaper closes pipe before reader drains, (3) helper encoding
loses output on fast exit. Read-only source shows reaper waits outputEOF, making
(2) less likely; (1) has an explicit competing case in Submit/drainInterrupted.
Main owns follow-up synthesis/docs/release. Carver diagnoses runtime read-only;
Peirce owns only the cmd diagnostic test's improved assertion/stress evidence.
No weakened assertions, sleep-based masking or blind CI rerun as a fix.
Runtime follow-up contract refinement from confirmed source evidence: stdout
consumer, not process reaper, owns turn settlement. Main exclusively owns
sessionruntime/starter.go; Darwin owns new public exit_drain_test.go; Peirce retains
cmd log regression only; Carver independently reviews ordering/cancellation;
Ohm reviews completed-write custody read-only. Consumer must drain buffered
event/final/terminal and steer acknowledgments before EOF, including interrupted
drain. Reaper still owns physical process/stderr completion, never waits for turn.
Unknown remains unknown without an exact terminal; no provider replay. Deferred
turn settlement covers every early consumer return; existing positive terminal
is not overwritten. Returning a successful write remains distinct from acceptance.

CI defect reproduced: original timing0/100 failures locally; public OnAccepted ->
exact Starter.Wait reap barrier90/100 failures, all lose modelEvents while retaining
acceptance and safe native class. Main independent barrier run10/10 RED. Minimal
starter.go change gives main both diagnostic cases x10 GREEN1.106s, identical
stress100/100 GREEN3.830s, full runtime/cmd race PASS7.492s/41.897s.
New public exit-drain matrix also checks event+EOF without invented terminal,
completed+exit, interrupted+exit, buffered steer ACK+completed, cancelled root with
buffered steer ACK+interrupted, and StopCurrent proof after reap. Six cases plainx20
and racex3 PASS20.179s; last two added after fix and no separate RED claimed.
Carver independently approves consumer ownership/no reaper wait cycle; cancellation
and write-error boundaries retained. Final complete gate/build is rerunning before
the follow-up commit/CI. No installed binary, config or state changed.

Follow-up final acceptance 18:54 UTC: complete make check-full PASS; cmd suite
plain25.349s/race43.466s, runtime race10.902s; final Carver review approve including
cancel/Stop cases. Rebuilt version unchanged (not previously installed):
bria SHA256 8ad43ff93c38f02eddeb52dc5ef50f54afe0f7804387228a66d6ca5cdbed415d;
both adapter hashes unchanged from above. Config check PASS. Exact follow-up
source set: starter.go, new exit_drain_test.go, cmd runtime_failure_log_regression_test.go,
this todo and STATUS_AND_NEXT.md. New commit/remote CI still required before install.

### Revision7 release completed - 2026-09-08 19:01 UTC / 22:01 MSK

Source follow-up00b26a7209f8def24ffae971a0b5168d30659f10 pushed and reread in
origin/main. [Stage1](https://github.com/Time4Mind/bria/actions/runs/34265856808)
PASS3m58s; [platform matrix](https://github.com/Time4Mind/bria/actions/runs/34265856861)
PASS including native macOS/Linux, four cross-builds and Docker. The previous
macOS failure was fixed, not masked by rerun. No further product changes.

Bounded install started18:59:13 UTC after fresh tool approval: exactly three
verified binaries copied to the new20260908-final-save-retry directory; old
PID89657 exited, current symlink atomically replaced, same launchd plist bootstrapped.
No manual config/state/journal writes, dependency installs, deletion of old
release/data or unrelated services. No new rollback backup was made.

Postflight: gui/501/com.time4mind.bria.v2 stays running/PID89675/runs1/no exit;
oldPID absent, only newPID owns the existing instance lock. Installed version and
all three SHA256 values equal the final local build (bria8ad43ff..., adapters above).
Config/plist/settings bytes unchanged. Two live Codex adapter children use the
exact new release directory. Installed binary getMe returns expected bot identity.

Repeated physical JSON comparison with preflight copies preserves all5 session
identities,5 cards,135 ordered existing history items and all15 completed input
records byte-for-value, with unchanged settings. State now has3 ready/2 archived,
no awaiting_recovery: the previous emergency session recovered automatically.
Fresh logs: telegram.flow_ready18:59:16.777769 UTC, two startup_recovery/recovered
events18:59:18.115546 and18:59:18.917541. No fresh classified error in the observed
post-start window. This is bounded postflight, not perpetual monitoring.
Independent Bernoulli postflight18:59:13-19:00:54 UTC confirms the same values,
zero fresh errors and unchanged full telegram_ui. The formerly awaiting Codex
session retains its native identity, generation advances1->2, recovery target clears.
Log retention reduced old records; comparisons use only the post-start cutoff.

All current requested implementation/release obligations are complete. This record
is the final documentation-only follow-up; it does not change installed code.
Boundary: no live disk fault was injected, no new user prompt or outgoing test
message was sent, and Telegram card appearance was not manually inspected.
Local actual HTTP/store/runtime regressions cover those changed flows; readiness
and recovery logs do not independently prove the owner's visible card interaction.

Documentation-only938f24706dab5dbf2d49711ddf4e8c7abe8a5957 is remote verified,
but its extra Stage1 run34266640937 exposes a pre-existing nested-Claude heartbeat
test instability: process_nested_unix_test.go87 sees timestamp then empty after
Abort. No product file differs from the green/deployed00b26a7. Service stays healthy;
do not silently claim this last CI green or redeploy unchanged binaries.
Read-only diagnosis: one heartbeat writer uses truncate-then-write, and old helper
readers accept empty snapshots. Exact CI interleaving remains inferred; process
survival is not proved by an empty file. Darwin owns only nested process fixture
and optional heartbeat test: atomic increasing counters, valid samples before kill,
bounded continuous quiescence after kill, negative live-writer control; no production
change. Peirce measures original stress; Carver independent review. Main owns
records/final CI iteration. Retain successful deployed-code evidence separately.

Test-only follow-up ready locally: atomic publication regression rejects old
truncate-in-place via retained inode; invalid/empty samples are errors; negative
live-publisher control must time out, not succeed. Two valid increasing samples
precede Abort, then100ms uninterrupted quiescence is required within1s. Exact
request/binding Abort cleanup now covers early fixture failure. Original Abort
deadline remains unchanged. Baseline Darwin100/100 passed, so the rare Linux
interleaving is not claimed locally reproduced. New fixture RED->GREEN, original
scenario plainx50 and race-controlsx20 PASS, final package plain1.136s/race0.959s;
main independent package race0.905s. Carver reviewed assertions; cleanup finding
fixed and reread. Only two test files and task records differ from938f247;
full gate and rebuilt-hash equality will confirm unchanged installed product.

Test-only final local gate19:13 UTC: make check-full PASS, independent review
approve including exact Abort cleanup. Rebuilt trio hashes equal the installed
8ad43ff.../9ac46e30.../0964ef3d...; no new deployment is required. Exact final
follow-up contains only process_nested_unix_test.go, nested_heartbeat_test.go
and the two task records. Current remote check runs for that commit are the
external CI receipt; never attribute the older938f247 failure to the fixed test.
No product obligation was dropped or deferred; the runtime release remains
verified and unchanged. Working state/config/history untouched by this test fix.

### Current release condition audit - 2026-09-08

Artem now explicitly requests push and restart if the entire plan is complete,
and asks whether repeated authorization is coming from memory. Exact result of
this audit: verify that condition against current source/todo, identify the actual
authorization source, do not treat a conditional release as unconditional approval.
Main owns this small read-only audit and task-record update; no parallel code zones
or product implementation. Sources: current AGENTS.md, lifecycle documentation,
controller final-store failure branch and recovery admission. Timezone Europe/Moscow;
unit: whole plan vs remaining failure case, and one release authorization.

| ID | Obligation | Status / evidence |
| --- | --- | --- |
| A25.R1 | Verify whole-plan completion before conditional release | condition not fully met: F2 complete, F1 remains implemented safe blocking but no history-only retry while provider lives; controller persistFinal error returns Unknown before Finish, recovery accepts AwaitingRecovery only |
| A25.R2 | Identify whether memory causes redundant approval requests | checked: operative explicit release rule is current AGENTS.md Safety and writes, not a canonical-memory rule; no memory correction or policy relaxation performed |
| A25.R3 | Conditional push/restart | new user instruction recorded as release intent after full completion; no push/deploy performed while F1 disposition is unresolved; retain exact-target/preflight/postflight requirements |

Do not repeat the old unconditional statement that the user has never authorized
release: the latest request is conditional authorization. Do not call F1 accepted
or solved merely because the previous handoff listed it as a separate limitation.
Need owner direction on completing F1 before release versus explicitly accepting
that limitation. No installed state, source behavior, Git refs or memory changed
by this audit; only this todo and the current pointer are updated.

### Current agreement revision 6 - A25.F2 accepted for local implementation

Artem accepted the flow comparison and explicitly authorized taking it into work
if it is the sole blocker. It is the last open queue-policy decision; A25.F1 remains
an explicit separate storage-failure limitation, not silently included or closed.
Result: a confirmed failed/interrupted provider turn ends that request and lets
later queued messages proceed in order without replaying the old request. Unknown
outcome and unaccepted/legacy failures remain blocked unless exact new proof exists.
Sources: current code, accepted four-row flow, revision 5 evidence. Unit: one exact
input identity/sequence and the same session's ordered queue; no business period,
Europe/Moscow for handoff. Local code/tests/docs/build only; preserve dirty A17-A25,
no install/restart/push/live inputs/state/messages or unrelated fixes. Acceptance:
public RED->GREEN main/steer Stop A -> queued B -> confirmed terminal -> B once;
known failure equivalent; unknown/no-proof remains blocked; restart/reopen and
late exact recovery, duplicate callback, failed storage and no-replay checks;
independent review, complete local gate, physical executable version/hash.

Shared bounded representation: add terminal_failed outcome/phase distinct from
legacy failed; only a correlated provider terminal or exact native failure proof
can produce it. Do not convert all failed records or relax Unknown. Native recovery
DTO gains TerminalFailureProven bool, valid only for Failed with nonempty exact
TurnID; no guessed text matching. Existing successful-final ordering is unchanged.
Old binaries may reject the new phase: document the rollback boundary, no live
state migration. Main owns synthesis/docs/registry/root and final verification.

Coverage before fanout, disjoint ownership: Ohm owns messagejournal/durableflow/
durableinputbridge/durablecomposition and their tests; Carver owns turnprocessing
DTO, telegramcontroller production and scoped controller tests; Darwin owns native
recoveryruntime, sessionruntime/protocol.go DTO and recoverycomposition with tests.
Peirce owns only new cmd/bria/terminal_queue_continuation_test.go actual end-to-end
tests. Bernoulli first audits enum consumers read-only then reviews frozen backend;
Anscombe reviews frozen controller/recovery read-only. Main coordinates shared API
before dependent edits. Stop each zone at implemented public behavior plus scoped
plain/race/vet; no duplicate broad scans or external sources. Full gate once after
integration. Further changes require owner synthesis, not unilateral scope drift.
Consumer refinement: main also owns coordinatorbundle/bundle.go and its tests;
its explicit phase validator must preserve terminal_failed through coordinator
state transfer while retaining old failed/unknown values and rejecting unknown
phases. This is a required existing consumer, not a new feature or external transfer.
Main also owns backupsource/source.go and terminal_failure_test.go: backup validation
accepts the new settled phase but does not export it as undelivered/retry input;
legacy failed/unknown and pending records retain exact identity/order. No live
backup or restore is authorized or performed; tests use isolated synthetic state.

| ID | Obligation | Status / acceptance |
| --- | --- | --- |
| A25.F2.1 | durable proven failure, compatibility and no replay | local complete; reopen/legacy/unknown/duplicate/lane and bundle/backup consumer tests, independent review and full gate PASS |
| A25.F2.2 | live main/steer classification, UI copy and wake | local complete; known Stop/error -> next input once; failed history/custody/journal commit leaves B Pending; actual-runtime fixture race and full gate PASS |
| A25.F2.3 | exact late recovery proof | local complete; physical native partial/conflict/exact proof and higher-generation recovery race PASS; no guessed legacy conversion |
| A25.F2.4 | integration/review/full gate/build/handoff | local complete; two final independent approves, make check-full PASS, physical 20260908-terminal-queue verified; no live deployment, A25.F1 retained |

Revision 6 integration evidence: new coordinatorbundle Digest consumer regression
RED->GREEN, bundle/transfer race PASS without converting legacy/unknown phases.
Darwin's exact failure-proof DTO/reader/reconciler frozen; native interrupted plus
conflicting complete, partial, malformed, missing binding and legacy receipts
covered; Anscombe independent narrow approve with fresh race/vet and unchanged
hashes. Peirce's actual Starter/controller/lifecycle/dispatcher/journal test observed
Interrupted RED->GREEN: confirmed Interrupted/Failed resolves main+steer to
terminal_failed, queued B executes once automatically; EOF and attachment completion
failure resolve both Unknown and leave B pending. Reopened journal/history and
Stop/Error notices are asserted; all four scenarios race x3 PASS. Controller now
shares its final outcome only after attachment custody; finalization gap and
final-before-Finish ordering remain covered. Backend/controller handoff and final
independent reviews/full gate remain mandatory; these scoped tests are not release.

Revision 6 finalization admission regression: reviewer found that an already leased
next input B waits for A, then ignored A's final Unknown outcome and could start
from Ready after history/custody failure. Required correction within the same
approved flow: a definitely unsent B remains Pending, not a new ambiguous input.
Carver owns controller/turnprocessing ErrInputDeferred and blocked-signal admission
plus safe reset after committed recovery; Ohm maps the exact unaccepted receipt to
InputProcessDeferred and releases only that pending lease. Accepted/mismatched
tuples must never be requeued. This refines the existing no-replay/unknown-block
obligation, not a new product decision. Anscombe reviews reset locations; Bernoulli
waits for renewed backend freeze. Public RED->GREEN and integrated race required.

Physical native recovery consumer check: Peirce added only
cmd/bria/terminal_native_recovery_test.go. Six cases join real receipt file,
partial JSONL, fresh native reader, recovery bridge, durable flow, reopened journal
and history. Partial/conflicting evidence keeps A accepted/unknown/legacy failed
blocked; complete exact interrupted proof settles A and leases only B once.
No replay or native/history mutation. Main rerun race x3 PASS (2.924s); this is
synthetic local integration, not live provider or Telegram acceptance.

Second finalization ordering regression: Bernoulli independently reproduced a
real controller/Flow/journal RED under race: A's result signal releases already
leased B before OnCompleted durably commits A; injected ResolveAcceptedInput(A)
failure leaves physical A=accepted while B reaches completed. Carver must gate new
root admission on completion-commit settlement as well as provider/history/custody,
including accepted steers without deadlocking their shared result wait. Definitely
unsent B remains exact Deferred/Pending on any failed commit. This is the approved
save-before-continuation acceptance boundary, not permission for replay or live
repair. Independent review request-changes remains open until regression GREEN.

Bounded ownership refinement before dependent implementation: Darwin owns new
internal/turnadmission (sealed root/steer commit barrier, public tests, 120-line
production budget). Existing turncompletion retains its 70-line result-publication
responsibility unchanged. Carver owns controller integration; Peirce owns only
cmd/bria/completion_commit_queue_test.go and existing new integration fixtures.
Main owns package registry/import boundary and docs. Admission registers steering
attempts before sending, seals membership, and settles only after root and every
registered ticket finish; idempotent completion and sticky errors, no WaitGroup
Add/Wait race. Result notification is separate so commit callbacks cannot deadlock
waiting for admission. Existing package budgets are not increased.
Controller budget remediation uses a pure responsibility extraction, not relaxed
limits: main owns new internal/telegramsemantic (canonical action constants and
unchanged validation/classification, 200-line budget, characterization tests).
Carver alone replaces the controller's constants/validators with aliases/delegates.
Main owns both registry entries and import boundaries. New admission helper's
public RED->GREEN, race x100/vet PASS; main fresh admission+semantic race x10 PASS.
The helper alone does not prove controller commit ordering; integrated checks and
renewed independent review remain mandatory.

Final controller freeze: 5479/5500 production lines; scoped controller/turnprocessing
plain/race/vet PASS. Public RED->GREEN covers failed main or steer completion commit,
in-flight steering acceptance, Unknown finalization and recovery during/after cleanup.
Main actual cmd/bria integration race x3 PASS (24.621s), pattern TestCompletionJournal
plus finalization/native/terminal scenarios. This includes physical A/steer journal
failures with already leased B, no B submission, exact unleased Pending B, and exact
higher-generation recovery; stale refresh cannot erase a new unresolved outcome.
Main architecture and scripts checks PASS. Complete gate and final independent
verdicts are still pending; no earlier GREEN substitutes for them.

Revision 6 final local acceptance - 2026-09-08 17:17 UTC (20:17 Europe/Moscow):
Anscombe approved frozen controller/proof scope, own scoped race/vet PASS and
unchanged hashes. Bernoulli approved backend/helper/consumer/semantic and commit-gap
scope, actual-runtime main/steer fault + root regression race x3 PASS (10.500s),
unchanged hashes; no open findings. Main make check-full with
VERSION=20260908-terminal-queue exited 0 and printed executable-trio acceptance OK.
Includes policy/links/secret scan/format, four-target architecture, full plain/race,
vet, operational packaging and local physical build. Synthetic external-process
and loopback fixtures only; not a real Telegram/model session acceptance.

Reread physical artifacts: all three bin files are Mach-O arm64; bria version
reports 20260908-terminal-queue. SHA-256:

- bin/bria: ed2e34c900639ad19a503c4d80dec3cf85410ae35e39068370693c49d98a0579
- bin/bria-codex-adapter: 9ac46e30e6ffe3f21f89a3467bb6aed5cb540af22b0c8fcb8e28d631f1fa38f9
- bin/bria-claude-adapter: 0964ef3d792242e2bc3fcf3f606412cdb70b3d8c39074a379dc8061694159e53

Adapters have no version command; unsupported `version` probes returned generic
failure/usage, not a version receipt. Their executable acceptance, format and hashes
are the physical evidence. Installed current symlink reread remains
/Users/a-s-nosko/.local/opt/bria-v2/releases/20260908-card-order. No commit, push,
installation, service restart, live input/card mutation or outbound message was
performed. A25.F2's local implementation obligation is complete; deployment/live
acceptance needs a separately bounded authorization. A25.F1 remains the documented
live-provider final-history retry limitation; no silent scope reduction or claim
that this queue fix repairs it.

### Current agreement revision 5 - full implementation authorized

Artem explicitly accepted the full estimated scale and requested implementation.
Deliver all A25 blocks, no proposed reductions: bounded native records preserving
significant events/partial writes/order; configurable post-wrap5/10/20/40(default10)
spoilers at100Unicode chars/line with readable blocks and explicit truncation;
retain enough text for40lines through actual persistence/protocol; recovery visible
and selectable for history but not ordinary input, consistent foreground after
menu/restart, history/switch/recover/archive actions; exact-request proven final
restoration without replay, unknown remains honest; payload-free exact cause and
correlated lifecycle/UI/delivery diagnostics; reuse voice newline fix. Harness
recurring-error rule retained. Existing size gate satisfied for proposed full
fixes, not unrelated new systems. Source current repo and all explicit approvals;
timezone Europe/Moscow for incident reports, synthetic cases for tests.
Authorized local source/tests/docs/build and bounded read-only original evidence.
No install/restart/push/live state or card writes/provider input/Legacy/memory.
Preserve A17-A24 dirty work. Full gate and physical build before local completion;
external visual/service recovery acceptance remains separately gated.

Coverage/ownership before fanout: main synthesis/docs/architecture registration,
runtime error telemetry and final UI/backend integration. Parser worker owns
nativejsonline + nativetranscript/reader.go and parser tests (NOT parse.go metadata).
Renderer worker owns cardtranscript and dedicated pure text helper if needed;
proposes metadata boundary changes to main rather than editing other packages.
Settings worker owns settings/codec/port/composition/view and signed settings
callback mapping; temporary controller.go ownership until explicit handoff, main
does not edit it concurrently. Recovery worker owns recoveryruntime, nativeadapter
receipt correlation and sessionruntime/protocol.go recovery DTO, recoverycomposition
and durablecomposition recovery bridge, not controller/storage. Main resolves
crossings first. Useful independent work only; read-only independent reviews after
workers deliver. Scoped tests during development, full final gate once integrated.

Revision 5 ownership refinement: Darwin owns supervisioncomposition manual recovery
and typed controllertelemetry recovery events, optionally sessionrecoverycontrol;
main retains root wiring. Bernoulli owns runtime_diagnostic_test.go and recovery
storage/cardhistory tests only; production changes reported to main. Main owns
storage/cardhistory final restoration and runtimediagnostic extraction, currently
passing scoped storage/sessionruntime/observabilitycomposition tests. Recovery UI
still RED (awaiting session missing); controller ownership remains with settings
worker until explicit handoff. No external writes.

Revision 5 integration update: settings worker handed off controller ownership.
Main now owns recovery UI, root wiring and all card-consumer Recovery flags.
Settings signed callback/store/reopen/Rich/race passed. Recovery-card menu/restart
visibility regression RED->GREEN; normal/native input guards and atomic new-prompt
rejection added, manual-action tests ongoing. Real A24 read-only replay now emits
13 events then EOF-like empty poll at both default and 3MiB settings, no ErrLimit;
old encoded-history byte match is intentionally false after the new text codec.
Native adapter tests pass outside sandbox using isolated temporary tmux fixtures.
Backend follow-up remains mandatory: retry persisted InputUnknown against new
exact final+completion proof, require complete scan rather than bounded-prefix
proof. No final acceptance yet. New helper boundaries are registered without
raising existing package caps; tests cover extraction contracts.

Independent reviews: Carver reviews UI/storage and lifecycle ordering; Bernoulli
reviews parser/text/runtime diagnostics; Anscombe reviews exact receipt/recovery
and InputUnknown reconciliation. Peirce owns only the new complete voice-lifecycle
wire regression. Darwin owns automatic post-commit notifier and typed telemetry.
Main reproduced an additional accepted-exit race: worker Finish changed Running
to Ready before supervisor accepted-turn reconciliation. Regression
TestAcceptedUncertainExitCannotFinishLifecycleBeforeSupervisor RED->GREEN after
skipping normal Finish for accepted runtime errors; recovery target remains intact
for supervision. Error copy now states unknown rather than falsely not executed.
ProviderFailure typed flow event bridges runtime cause to recovery/UI session refs.

Revision 5 review follow-up: no new scope, regressions in the agreed boundaries.
Confirmed failed/interrupted terminal results now retain normal lifecycle Finish;
only unproven accepted exits remain unresolved. Carver preserved correlated
interrupted proof during cancellation drain; real Starter/controller/store cases
cover Ready/Archived versus Running/ClosingAfterWork. Bernoulli fixed a CRLF
prefix-offset defect in the tool codec (ASCII/emoji/40-line round trips).
Peirce's actual Rich wire pending/recognized/working/final lifecycle test passed
three race repetitions, including the isolated final page. All are local fixtures.
Still mandatory before closure: correlated completed receipts with partial native
tails must not seal a request without final proof; async acceptance must remain
InputAccepted until the actual terminal outcome and successful final persistence.
Main owns controller completion bridge and final-store error propagation; Ohm
owns exact journal/flow callbacks and partial-tail recovery; Darwin's turncompletion
signal is handed off. Peirce owns only accepted_terminal_journal_test.go integration;
Anscombe reviews controller ordering read-only, Bernoulli reviews stable backend
read-only. First full gate passed plain tests/vet/operational checks but was
interrupted during race after these findings; it is NOT final acceptance and must
be rerun in full on the integrated version. No install or service changes.

Completion follow-up: real Starter/controller/flow/journal integration now proves
accepted main and steer records remain accepted until a terminal result; final
is physically persisted before the async completed callback. Final-store failure
RED (Ready/Archived) became GREEN (Running/ClosingAfterWork + InputUnknown,
no final notification) after moving persistence before lifecycle Finish.
Fresh race run of this integration and terminal-proof scenarios passed. Remaining
independent review findings are owned explicitly: Carver fixes durable acceptance
surviving a subsequent attachment-custody failure; Anscombe fixes steer shutdown
waiting for the producer's bounded terminal drain rather than publishing unknown
on waiter cancellation. Backend has handed off to Bernoulli for read-only review.

Explicit operational limitation (read-only Darwin probe): a final-storage failure
while the provider process remains alive has no immediate live history-retry API.
It safely retains Running/ClosingAfterWork + InputUnknown, reports the storage
failure and never replays; hazardous startup reconciliation remains available after
the cause is fixed and an independently authorized restart. Do not silently kill
or restart a healthy provider to manufacture recovery. A future live retry of
history persistence is separate follow-up, not claimed by this local acceptance.

Final async boundary checks: attachment-custody failure after durable acceptance
now preserves Accepted and produces exactly one Unknown callback (public unit RED
and actual controller/flow/journal race GREEN). Shutdown main/steer waits for the
same bounded producer drain: confirmed Interrupted -> both Failed; EOF/timeout ->
both Unknown, no duplicate callbacks. Independent backend review's late completion
without this processor's acceptance was reproduced and fixed with an atomic guard.
Finalization race RED->GREEN: a next input waits for prior final persistence and
Finish, then rereads live session/binding/closed and starts only from Ready.
The final main race integration passed; a cancellation variant and final reviews
remain before full gate. Existing package budgets stay unchanged: neutral options
and history contracts are in telegramcontrolport, pure lifecycle copy is in
telegramsessionview; legacy aliases preserve call sites. Late Running input dispatch
is covered; terminal-triggered wake for queued Stopping input is being integrated.

Decision pending A25.F2 (asked asynchronously, no default change): existing journal
policy blocks InputFailed as well as Unknown until explicit RetryInput, but the
supported UI has no retry action. Correct terminal accounting exposes this after
a known failed/interrupted turn. Proposed separate semantic decision: let later
messages proceed after a verified terminal without replaying the old request;
Unknown and failed unaccepted handoffs remain blocked. Do not silently reinterpret
legacy failed records or change this policy without Artem's answer. Current local
checks retain the existing failed-lane policy; the limitation must be in handoff.

| ID | Current obligation/status | Acceptance |
| --- | --- | --- |
| A25.1 | locally implemented; scoped plain/race PASS | original 2.32MB record replay emits 13 events without ErrLimit; framing/partial/malformed/cancellation coverage |
| A25.2-A25.3 | locally implemented; signed UI/store/reopen/Rich/race PASS | choices 5/10/20/40; 4000/4001 Unicode retention end-to-end; CRLF prefix regression |
| A25.5-A25.6 | locally implemented; independent review approve | exact correlated final once before completed; partial tails/unknown block proof; main/steer/drain/storage/recovery UI regressions; A25.F2 decision below remains separate |
| A25.7 | locally implemented; physical JSONL/flow tests PASS | classified runtime failure and correlated recovery/selection/delivery, payload/secret absence |
| A25.8 | Harness rule done previously | preserve rule |
| A25.9 | actual Rich HTTP lifecycle race PASS | pending/recognized/working use one hard newline; final starts on separate page |
| A25.10 | full scale explicitly approved | reductions not applied |
| A25.4 | local full gate/build PASS; external acceptance not authorized | physical darwin/arm64 executable trio and version/hash below; service unchanged |
| A25.F1 | explicit operational limitation | no immediate live history-write retry while provider remains alive; safe Unknown, no replay; separate follow-up |
| A25.F2 | owner decision pending | legacy failed-lane policy retained; asked whether later messages may proceed after known terminal without retrying the old request |

Final independent narrow reviews approve: Bernoulli acceptance guard, attachment
acceptance, final-before-Finish, late Running dispatch and post-commit wake;
Darwin finishing-input guard; Anscombe shutdown main/steer proof. Main freshly ran
the actual affected controller/Starter/journal/store cases under race. The complete
gate finished on frozen production sources: all plain tests, race, vet, architecture,
policy, secret scan and operational contracts passed, followed by physical build.

Local acceptance receipt, 2026-09-08 16:20 UTC (19:20 Europe/Moscow):
`env TMPDIR=/private/tmp GOMODCACHE=/Users/a-s-nosko/go/pkg/mod VERSION=20260908-recovery-tool-limits make check-full`
exited 0, `Bria executable trio acceptance: OK`. Escalation was limited to isolated
temporary tmux/loopback test fixtures and the local build, not the installed service.
`bin/bria --version` reread: `bria 20260908-recovery-tool-limits`; all three files
are Mach-O arm64 executables. SHA256:

| Local file | SHA256 |
| --- | --- |
| bin/bria | 98c28b0cea040307dc77a3a9ad2e142ff9684db9fa1b18ba065036190d76810b |
| bin/bria-codex-adapter | 9ac46e30e6ffe3f21f89a3467bb6aed5cb540af22b0c8fcb8e28d631f1fa38f9 |
| bin/bria-claude-adapter | 0964ef3d792242e2bc3fcf3f606412cdb70b3d8c39074a379dc8061694159e53 |

Installed symlink independently reread at handoff:
`/Users/a-s-nosko/.local/opt/bria-v2/current` still targets
`releases/20260908-card-order`. No commit/push/install/restart/provider input/live
card or state mutation. Dirty A17-A24 changes are preserved; the aggregate diff is
not an A25-only change count. Live Telegram reproduction is not claimed. Next owner
decision is A25.F2; deployment remains a separate bounded authorization, not an
automatic consequence of a successful local gate.

Older revisions below are history; their pauses/size-gate requests are superseded.

### Current agreement revision 4 - scope/size assessment before resuming code

Artem accepted the final voice block: exactly one visible newline after divider
through recognition/processing/card updates; reuse A21 code and verify complete
path; Telegram visual acceptance only after separately approved installation.
All discussed requirements are now recorded, none accepted by default. Next:
assess total local implementation and minimum solution against the explicit size
gate before resuming partial source edits. Main owns synthesis, recovery/unknown
outcome/logging assessment and docs; renderer worker read-only estimates its
remaining formatting/persistence/pagination boundary; settings worker read-only
estimates callback/UI/snapshot integration. Disjoint read-only zones, no code
edit authority in this assessment phase. Stop at grounded component/file map,
estimated scope and minimum alternative; if large or potentially large, obtain
owner approval before implementing expansion. Unit: whole A25 feature set,
source current repo + explicit approved blocks; no business period. No external
writes/live recovery/installation/restart/push/messages or memory changes. Prior
partial source remains unverified/unreleased, no unrelated cleanup.

| ID | Obligation | Status | Acceptance |
| --- | --- | --- | --- |
| A25.9 | Voice update spacing | requirements accepted; previous local regression only | reuse A21, complete voice path, external visual acceptance separately |
| A25.10 | Total implementation-size gate | substantial-expansion risk found; owner choice required | concrete missing final-recovery seam and minimum phased alternative below |

Assessment evidence (read-only source): recoveryruntime.NativeReader reads only
the native adapter receipt JSON; sessionruntime.ReconciledAcceptedTurn carries
MessageID and Outcome, not native turn identity/final text. recoverycomposition
passes these outcomes to supervisor; it does not rebuild missing card history.
Thus final reconstruction needs a new exact-request correlation/projection path,
not merely a status label. Existing supervisor tests already protect unknown from
restart/replay. Controller/node_scope and telegramsessions.Selectable currently
tie foreground selection to input readiness; view-only recovery requires separating
these concepts in restore/select/render/input paths, not just adding a button.
Existing telemetry has selection/archive/card-delivery chains, but runtime maps
most failures to provider_error and native adapter error classification lacks the
oversized-record cause. No current live logging completeness claim is made.

Minimum phased option proposed, NOT approved: finish bounded parser, configurable
readable spoilers, visible recovery/read-only history/switching, honest unknown
status/no automatic replay, narrow missing failure telemetry and voice regression.
Defer the NEW in-card recovery action that reconciles raw transcript and restores
a missing final, along with its exact-repeat confirmation flow, to a separately
estimated stage. This explicitly defers parts of A25.5/A25.6; no silent closure.
Already existing safe recovery mechanisms are retained, not removed or bypassed.
Physical external recovery/deploy remains separately unauthorized in either option.

Scope sizing is an engineering estimate, not measured finished diff; old A17-A24
dirty changes must not be counted as A25. Size gate is triggered by the missing
cross-component recovery seam, not a manufactured exact line count. No additional
production edits/tests/build/live calls in this assessment turn. Main requested
bounded renderer/settings read-only estimates and then stopped further exploration;
their final reports remain supplementary evidence, not acceptance of draft code.

Supplementary worker assessment received: settings/callback minimum is one cyclic
control and projection-only propagation: 6 functional production files (25-50
lines), estimated 8-11 files/60-140 changed lines including cap-preserving moves;
tests 180-350 lines. These are estimates beyond saved draft, not measured final
diff. Snapshot/callback routes remain incomplete. New settings field reads old
v1-v5 docs, but old codec rejects the new field, so binary-only rollback after
settings write is not a supported assumption. No deployment is authorized.

Renderer source bounds: native metadata 8192 bytes, persisted per-tool field6000
bytes, per-history record16384 bytes, pagination3000 bytes. 4000 emoji require
16000 content bytes, so remaining chain cannot promise all first4000Unicode
characters unchanged through present field caps. Worker estimates renderer-only
within byte caps100-200 production+250-450 testlines; full-preservation chain with
bounded fragmentation350-700 production+600-1100 tests, excluding settings/parser/
recovery/logging. Fragmentation is not chosen: main has not established it as
the minimal design, and it introduces history ordering/merging complexity.
Alternative requiring explicit owner choice: maintain existing safe byte caps,
decode before truncation, label source/storage truncation, and treat choice40 as
up to40 wrapped lines of available retained content (may be less for longUnicode).
Do NOT silently implement that relaxation or a fragmentation subsystem. Scope
gate now includes this renderer retention choice as well as deferred final recovery.

Revision 3 below is retained as agreement history; its requirements remain valid,
its statement that final requirements discussion is pending is superseded.

### Current agreement revision 3 - sequential requirements discussion

Source: subsequent explicit approvals in the current conversation. Parser block
is now accepted: bounded reading, preserved significant events, partial records
wait, malformed records fail explicitly, no loss/duplicates on retry/cancellation.
Recovery visibility block is accepted: retain marked session in list, stay on its
card after failure, disable ordinary input, allow history/switch/recovery/archive,
keep selection/card/buttons consistent. Recovery is an emergency state, not a
substitute for eliminating failures. Unknown-outcome block is accepted with an
implementation-size gate: reuse existing mechanisms; if changes are or may become
large, present size estimate and a minimal alternative for separate approval BEFORE
implementing that expansion. No automatic replay of an accepted unknown prompt;
verify exact request transcript and recover proven final/completion without rerun.

Logging block is accepted conditionally: first establish existing event coverage,
then reuse correlation and add only proved gaps. No claim that logs are complete
until a representative physical trace connects failure, lifecycle, selection and
delivery outcome. Repeated-error notification is an agent Harness obligation,
not an authorization for a new alerting service or merely suppressing log noise.
Requirements discussion continues; product implementation remains paused, partial
code is not accepted/released. Earlier scope records below are history superseded
by this revision; all external-write gates remain in force.

Current narrow write contract (main owner): update docs/HARNESS.md with the
requested repeated-error behavior; update this todo and STATUS_AND_NEXT pointer
to retain all above agreements. No production/test code, runtime, canonical pet
memory or service writes. Unit: rule restored through AGENTS -> HARNESS, trigger
on observed recurring errors, evidence-based owner notification and durable followup.
Acceptance: reread exact rule and pointers, positive/negative scenario review,
check-policy and diff whitespace. One indivisible instruction edit owned by main;
independent read-only review, no parallel editors. No live-log investigation
requested in this turn; current logging sufficiency remains unverified.

| ID | Additional obligation | Status | Acceptance |
| --- | --- | --- | --- |
| A25.5 | Recovery visibility and actions | requirements accepted; implementation paused | four agreed behaviors and emergency-only semantics |
| A25.6 | Unknown accepted request outcome | requirements accepted with size gate | exact-request evidence, no automatic duplicate, approval before large redesign |
| A25.7 | Logging coverage/correlation | conditionally accepted; verification pending | physical trace coverage before sufficient-logging claim |
| A25.8 | Harness recurring-error rule | done locally | reread, independent scenario/scope review, check-policy and diff whitespace PASS |

A25.8 acceptance: the mandatory AGENTS -> docs/HARNESS.md entry path now restores
four recurring-error rules. Independent reviewer confirmed positive trigger for
recurrent independent failures, exclusion of duplicate records of one failure,
evidence/uncertainty and open-todo requirements, no implied external alerting or
fix authority. Main reread the written rule; make check-policy (policy, links,
filenames/secret scan) and git diff --check passed. Only these three task/Harness
documents were changed in this turn. Product draft tests, live logging coverage,
deployment and service behavior were not revalidated or changed.

### Historical scope and partial implementation

Scope clarification after Artem asked whether remaining sections were approved
by default: NO. Explicitly accepted is spoiler100characters/postwrap5/10/20/40
default10 and readable text blocks/settings. Main had interpreted the acceptance
as including the native parser; that implementation is now PAUSED pending
separate approval. Remaining recovery/selection/unknown-outcome/logging sections
are not authorized. Initial contract below is historical superseded scope, not
permission to finish parser work. No external writes occurred. Workers asked to
pause and hand off their exact partial work; no completion/release claim.

Parser partial files: new internal/nativejsonline/line.go and
internal/nativetranscript/large_record_test.go; changes to reader.go and architecture
registration in scripts/check_repo.go/check_repo_test.go. New >1MiB final-following
test reproduced original ErrLimit before implementation; partial implementation
currently breaks two existing reader tests (error-class priority and failed-batch
atomicity). Must NOT release this draft. These are main-owned changes, not user
changes; retained for explicit choice to continue or remove, not silently merged.

Immutable contract: implement the agreed first block locally: bounded handling
of large native technical records without aborting an otherwise valid session;
readable tool text blocks with hard wrap at 100 Unicode characters and a total
post-wrap line budget selectable 5/10/20/40, default 10. Truncation notice is
outside content budget. Preserve event ordering, final/approval semantics and
stored settings compatibility. Source: Artem's block-1 agreement and latest
explicit confirmation of post-wrap counting, current repo and A24 evidence.
No analytics period; synthetic cases plus exact A24 read-only transcript replay.
Authorized writes: local code/tests/docs/build only. No live config/state writes,
provider input/recovery, deploy/restart/push/messages, Legacy or memory writes.
Preserve prior dirty A17-A24 work. Other recovery/UI/logging blocks remain backlog,
not authorized by this block. Main owns synthesis and parser; all workers receive
this same contract. Acceptance: public red/green, setting persistence+UI to card
wire, single/multiline Unicode boundaries all choices, >1MiB technical event and
following final/approval, partial and malformed records, race/architecture/full
gate and physical build. No external visual acceptance claimed.

Coverage before fanout: main owns internal/nativetranscript and integration/docs;
renderer worker owns internal/cardtranscript only; settings worker owns settings,
settingscodec, settingsport, settingscomposition, telegramsettingsview and the
controller settings plumbing only (not transcript parser/renderer). Renderer API
adds configurable line limit via Block.ToolLines; defaults zero to 10. Main handles
remaining cross-boundary acceptance. Workers must coordinate before any crossing,
do not raise architecture caps. Stop at contract acceptance, no unrelated cleanup.

| ID | Obligation | Status | Acceptance |
| --- | --- | --- | --- |
| A25.1 | Bounded large native events | in progress | no lost following events; malformed fail closed; partial safe |
| A25.2 | Spoiler formatting/limits | in progress | 100 runes, 5/10/20/40 post-wrap lines, text blocks, notice |
| A25.3 | Persisted setting and UI wiring | in progress | default/compatibility, all choices, reload and projection |
| A25.4 | Integration and handoff | pending | full gate/build, scope and uninstalled boundary |

## A20 rebuild affected Bria session - 2026-09-08

Contract: use the name Bria (not a separate "v2" product); the existing v2
filesystem/service suffix is technical and is not bria-legacy. Explain that
retaining an older executable is only rollback protection, not another project.
Rebuild the existing affected session's card history from its exactly bound
native transcript, preserving all text, tools, prompts, native identity and
other sessions. No rerun/new provider session. Sources: current state, journal,
bound native transcript and A19-tested code. UTC evidence/Moscow report.
Latest read: affected session is ready, 55 history entries; service PID46960
still uses card-diagnostics. Scope requested: this session reconstruction;
prepare local dry-run and a bounded install/repair/card-refresh manifest.
Deploy, offline live-state replacement and visible Telegram card edit each
remain behind the exact manifest approval. No renaming service paths, Git push,
config/secret edits, unrelated session writes or legacy repository access.
Acceptance: all existing entries retained in source order, full plan on latest
rendered page, exact provider binding retained, external edit receipt and stable
Bria process. Do not equate a rebuilt state file with visible card recovery.
Coverage: main owns operational CLI/projection and synthesis; worker owns pure
rebuild planner and synthetic tests in .cache/history-repair/plan*.go only.
One read-only review after the bounded diff; independent zones, no shared edits.
Native/runtime payloads stay outside Git; reports contain counts/hashes only.

| ID | Result / acceptance | Status |
| --- | --- | --- |
| A20a | Explain naming/rollback and establish exact affected state | done |
| A20b | Tested lossless reconstruction and safe dry-run manifest | done; fresh dry-run, race and independent safety review |
| A20c | Separately approved install, offline repair and visible refresh | done; exact-hash state write and same-carrier Telegram edit confirmed |
| A20d | Physical state, visible final and service postflight | done; startup reread preserves repaired history and native session identity |

A20 dry-run evidence: all 55 existing entries preserved; five prompt groups,
50 exact native-event matches, 48 row moves across two groups and 50 restored
turn anchors. Source state SHA256:
`619606ee31a6fe3982c7c2b0a72fc053b0d74103b06dc8a651158f9953e250f4`.
Validated candidate SHA256:
`147146776cf682955186f1c7099373a45d6b5a075eeb0cd8b18a5d9bed0a04ff`.
Real controller projects 14 pages, with the retained 2812-character final
ending on the latest page. JSON comparison proves every protected field
outside this card's history metadata/page is unchanged. Fresh planner/guard
race tests pass, including all 720 permutations of six synthetic output rows.
The helper is local under ignored .cache/history-repair; native payloads and
the candidate state are not tracked or published.

Exact pending manifest: install existing verified trio under
/Users/a-s-nosko/.local/opt/bria-v2/releases/20260908-card-order; retain the
current executable directory solely for rollback. Separately authorize stopping
gui/501/com.time4mind.bria.v2, acquire its state lock, verify both above hashes,
create exclusive state.json.pre-order-repair-20260908.json backup, atomically
replace only /Users/a-s-nosko/.bria-v2/state.json with the validated candidate,
edit only this session's existing Telegram carrier and bind its new keyboard
in state.json.callback-registry.json. Require exact returned Telegram text and
carrier, then atomically switch bria-v2/current and bootstrap the same existing
LaunchAgent plist. No new message, provider input, session, config or secret
mutation. An unknown edit response leaves the service stopped for inspection;
no blind resend or automatic rollback. Terminal: same native binding, 55 entries
in source order, confirmed visible final, installed version/hash and healthy
new PID/lock/getMe/logger receipt. This sequence was subsequently explicitly
approved through the operational permission requests and completed below.

A20 execution receipt (2026-09-08 UTC): the verified card-order trio was
installed and reread byte-identically; Bria SHA256 is
`59d7b6b7c71a136452b95e3162cd161db8b9ca65e43bf71a7f21fd901d7c9c5f`.
At 10:18:09 the old LaunchAgent was unloaded. The first repair attempt safely
refused the still-held lock before any write; read-only checks then proved old
PID exit, free lock, absent backup and unchanged source hash. Continuing the
same approved sequence created the exclusive backup and installed the exact
candidate hash. Telegram returned the same carrier and byte-equal requested
visible text; visible-text SHA256:
`5011d5e45df60950baef71b7f0bb05066152f3221851c087d7944e247c500209`.
Keyboard binding succeeded. No provider rerun or new Telegram message occurred.

Current now points to card-order. New PID 89657 is running with the actual
card-order executable and only it holds the instance lock; nonblocking flock
confirms exclusive ownership. getMe passes. New diagnostic ready record is
10:19:00.520128Z, schema 2. PID stable at 10:19:46 with no new critical event.
Startup reread: all 55 history rows, prompt/kind/turn arrays and same carrier
match candidate; last six kinds are commentary/tool/tool/tool/tool/final.
The full final has 2812 characters and final view is 14/14. Other session cards
are unchanged. The exact native session ID is retained; normal startup advanced
its runtime binding generation from 2 to 3. No new native user message after
the repair window. Config hash is unchanged. The backup hash equals the approved
original source. No Git push and no bria-legacy access or mutation.

## A19 preserve card event order - 2026-09-08

Contract: fix A18's loss of per-turn history order so commentary/tools precede
the final across repeated card commits. Source: explicit user fix request,
A18 physical evidence and current repository. No business data population;
test synthetic turns, including queued prompts and persisted state reopen.
Authorized: local source/tests/docs/build. Exclude live history repair, deploy,
restart, Git push, approval-policy changes and unrelated pagination races.
Acceptance: public-flow red/green regression for event order and final-page
content, relevant race tests, full local checks and physical build. Live
deployment and repair of already misordered history remain separately gated.
Coverage: main owns telegramflow production/test change and shared docs;
one independent read-only reviewer checks metadata preservation boundaries and
test coverage. Production fix is one indivisible commit boundary, so no second
source editor is useful. Reviewer may not edit files or access live runtime.
Main alone integrates; stop at bounded defect evidence and regression results.
No external calls needed; focused tests first, one full check before handoff.

A19 coverage amendment from a proved linked defect: preserving turn anchors
exposes SetCardPrompt's missing aligned append/trim, so fixing only commitCard
would reject the next prompt or misalign a 512-entry history. Same user-visible
order contract; no new external authority. A storage worker owns only
internal/storage/session_store.go and a new prompt-turn-key regression test;
main owns public flow integration test using the real SessionStore. Reviewer
remains read-only. All executors receive this amendment. Final full checks
must be rerun after integration; the earlier gate is not final acceptance.

| ID | Result / acceptance | Status |
| --- | --- | --- |
| A19a | Failing real send/commit + next-event insertion regression | done; both queued/non-queued cases reproduced wrong order and visible commentary |
| A19b | Preserve turn anchors; original regression and neighbors pass | done; commit and linked next-prompt preservation pass real-store integration |
| A19c | Independent review, scoped races, full checks and build | done; integrated final make check-full and 10-repeat public-flow race pass |
| A19d | Handoff with explicit installed-version/history-repair boundary | done; local build only, live deploy/repair require separate approval |

A19 red/green evidence: `go test -count=1 -mod=readonly ./internal/telegramflow
-run '^TestCardCommitsKeepProviderOrderAndVisibleFinal$'` failed on the original
commitCard in both subcases: newer outputs precede older ones and the rendered
latest page contains old commentary. After adding the omitted HistoryTurnKeys
copy, the same test passes. The test uses real signed flow preparation,
send/edit/commit, persisted FileStore reopen between events, prompt-aware
insertion and page projection; only external Telegram transport is simulated.
It asserts exact synthetic history order and final-page content, not just a
receipt or internal field. A queued prompt remains after the first turn's
events and its own final remains after that prompt. No live files were used.

Final regression now uses the real SessionStore plus production UI adapter,
not the earlier standalone FileStore: queued prompt arrives after the first
event's commit/reopen through SetCardPrompt. This reproduced the linked
alignment failure before the storage fix. It also repeats prompt replacement
and intermediate card commits without adding events. The integrated test is
green. Storage's new 4/512-row test separately reproduced rejection and silent
turn-key shift before the fix, then passed full row-wise reopen comparisons.
SetCardPrompt reuses telegramhistory.Append for bounded aligned append/trim;
no schema change or invented anchor repair. Existing package caps are intact.
Independent final review approved the integrated source/tests with no material
remaining findings; main directly inspected both production changes and tests.
Main verified final `make check-full` with VERSION=20260908-card-order: policy,
source secret scan, formatting, architecture, all tests/race, vet, operational
packaging, build and executable-trio acceptance passed. The public real-store
order/visible-final regression also passed `go test -race -count=10` separately.
Final production storage race suite passed in 28.370s and flow race in 4.240s.
The native macOS arm64 trio is in bin; installed current still points to
20260908-card-diagnostics. No push, installation, restart or live-history write
performed. Old misordered history is not automatically repaired by this fix.

Proposed deployment, not yet authorized: install the verified trio in
/Users/a-s-nosko/.local/opt/bria-v2/releases/20260908-card-order, preserve
20260908-card-diagnostics, atomically switch only bria-v2/current, and restart
only gui/501/com.time4mind.bria.v2. Terminal: installed hashes/version, stable
new PID, exclusive instance lock, getMe and fresh diagnostic-ready receipt.
Do not change config/secrets, manually send messages or rewrite live history.
Any history repair needs its own exact preview and approval.

## A18 missing final / frozen card diagnosis - 2026-09-08

Contract: explain the latest active session's missing plan/final and stopped
card updates after A17. Sources: live Bria v2 logs/state and the exact bound
provider transcript, then current repository code. UTC evidence, Moscow user
timeline. Unit: latest affected turn through provider final, durable state and
card delivery; exclude other bots and unrelated sessions. Read-only runtime
and source investigation; only this local diagnostic journal may be updated.
No fix, restart, deploy, push or outgoing message authorized. Acceptance:
identify the failing boundary with actual output, distinguish proved facts
from hypotheses and name the next correction/probe. Main owns live evidence
and synthesis. Initial cheap probe needs no fan-out; independent code tracing
may be assigned after the affected path is known. Hypotheses: provider stopped
without a final; final exists but was dropped by capture; persisted final did
not reach the card; service/transport failed. Status: in progress.

A18 coverage refinement: main owns live transcript/receipt/state comparison
and nativeadapter/nativetranscript boundary; one read-only worker owns the
downstream provider-final to history/card path (turnprocessing, sessionruntime,
telegram completion/prompt composition). No edits or runtime access by worker.
Stop at exact branch evidence; no expensive external calls. Main integrates
the two boundaries and alone updates this journal. Initial evidence: provider
has a full final at 08:33:16Z, while persisted card history ends in commentary.

A18 conclusion: diagnosis complete; no product/runtime changes. The current
process is still PID 46960/runs 15 and the installed binary matches local bin.
No critical log events since the A17 restart. Latest affected prompt entered
the provider at 08:31:37.807Z; native final_answer at 08:33:16.829Z and
task_complete at 08:33:16.878Z. Bria provider success is 08:33:17.035026Z;
session ready is 08:33:17.043575Z. The journal's final output is confirmed and
its decoded 2812-character payload exactly equals both the native final and
stored card history index 49. Nothing was lost by the model or adapter.

Five earlier entries appear after that final: four tool records and the
original commentary at index 54. Stored card has page 14/14, follow_latest=true,
but no history_turn_keys field. Final-triggered send was confirmed at
08:33:17.392442Z and state commit at 08:33:17.453274Z. The latest confirmed
outbound text contains the old commentary, not the plan. Later previous/next
page callbacks (08:58:04-06Z) and menu callback (09:31:34-35Z) completed; this
is a wrong-history-order rendering failure, not a stopped service.

Root mechanism independently read by worker and verified by main:
telegramflow.commitCard builds a replacement Card and copies History,
HistoryKeys and HistoryKinds, but omits HistoryTurnKeys (flow.go:1372-1386).
SetCard therefore erases the per-turn tail anchors on successful commits.
telegramhistory.InsertAfterPrompt (history.go:43-65) then falls back to the
original prompt and inserts newer output ahead of existing output from that
turn. A commit between groups of events is enough; concurrency is not required.
Storage Clone does preserve this field, so serialization is not the culprit.
Final delivery reprojects history (telegramcompletioncomposition:142) and
opens its latest page (telegramui.ProjectActiveFinal), rather than sending
the journal's final text directly. A confirmed final receipt thus confirms
transport of the wrong last page, not visible delivery of the full answer.

Recommended authorized-next fix, not implemented: preserve HistoryTurnKeys
at card commit; add deterministic insert/commit/order regression through the
real card store and final-page consumer; separately recover this already
misordered history from the intact journal/native sequence with explicit
runtime-write approval. Existing metadata-preservation test omits this field.
No new regression test or mutation was run in this diagnosis-only task.
The exact timing of every in-memory batch remains unrecorded; physical state,
journal order and the deterministic code defect establish the failing boundary.

## A17 logging implementation contract - 2026-09-08

Result: safe typed callback rejection reasons and correlated card transport /
binding / page-commit events, preserving all existing button behavior. Source:
A16 incident and Artem's logging-first request. UTC event timestamps, Moscow
operator interpretation. Cover invalid/expired/stale/replayed callbacks, menu,
pagination, background refresh and failures; never record text, callback_data,
credentials or raw user/chat identifiers. Acceptance: red/green persisted-log
tests, cross-component trace ordering and redaction tests, scoped race tests,
full release gate and installed logger startup receipt. A17a main owns flow
instrumentation, new telegramtrace package, scripts and shared docs. A17b worker
owns telegrampipeline plus new callbackdiagnostic typed-error package. A17c
worker owns observability and safelog output/allowlist/tests. Disjoint source
ownership; main synthesizes. No changes to callback acceptance, retry, lifetime,
pagination semantics or approval policy. Existing package caps remain unchanged;
small coherent diagnostic types/formatting live in explicit lower-level packages.
Workers stop at focused red/green tests; no external writes or runtime reads.
User subsequently requested immediate restart with these logs: main will build
the verified trio and deploy only Bria v2 to release
/Users/a-s-nosko/.local/opt/bria-v2/releases/20260908-card-diagnostics, preserve
current 20260908-architecture-races, switch current atomically and restart only
gui/501/com.time4mind.bria.v2 after the exact write approval. No Git push or
manual Telegram message. Terminal: installed hashes, stable PID/lock/getMe and
new diagnostic-ready event physically read from the live log. User will repeat
the pagination scenario. Status A17a/b/c: done, integrated and release verified;
A17d live install: done, installed logger and service postflight verified.

A17 local evidence: typed failures preserve errors.Is and original error text;
observer writes only closed reason codes and per-process HMAC references.
Physical JSONL integration reproduces a callback from a mismatched card and
joins its rejection to the delivered button manifest, without raw token or
message contents. Red/green tests cover missing diagnostic stages, callback
rejection reasons and edit-failure carrier correlation. Race tests pass for
all six changed runtime packages. Writer startup failure, queue overflow,
write-failure recovery, timestamp preservation and the 32-button limit are
covered. Native `make check-full` passed on the final source tree, including
full tests/race, vet, policy/secret scan, formatting, architecture, operational
contracts and executable-trio acceptance. Native runtime is macOS arm64;
cross-platform dependency graphs passed, but this local change was not pushed
and has no new remote CI run.
Preflight: existing Bria v2 PID 54746/runs 14; no running/starting session;
settings validate read-only and current sessions validate on an isolated copy.
No config, credentials or runtime state were manually changed.

A17 installed postflight (2026-09-08, UTC): approved bounded install and restart
began 08:20:21. `current` rereads to `20260908-card-diagnostics`; all three
installed binaries match the verified local build byte-for-byte. New Bria SHA256:
`7fb7238db4650c5465d60cbd1f1957844c457e6a42b9e2f150b43bb6c3382dca`.
Codex/Claude adapter hashes are unchanged from the previous release. The old
release is preserved. Config hash is unchanged. Launchd has PID 46960/runs 15;
same PID remains running at 08:21:28. `lsof` identifies the new release binary
and only this process on the instance lock; nonblocking flock proves exclusive
ownership. Read-only Telegram identity/getMe succeeds. No new critical event
since restart and no queue-loss/write-failure notice in this diagnostic run.

Physical live output: `state.json.logs/service.jsonl` contains version-2
`telegram.flow_ready` at 08:20:26.472687Z. The same run has 12 detailed events
at 08:20:36-08:20:46: two edits and one send, each with transport completion,
finalize-start and state commit; card/button references and page coordinates
survive the safe-log encoder. No manual message or button click was sent by
the agent. This verifies runtime logging, not repair/reproduction of the
pagination defect. User will repeat the interactive scenario and identify its
incident time. Source/tests/docs remain local and uncommitted; no new Git push.

Follow-up A16 (2026-09-08, 10:42 Europe/Moscow): Artem reports apparent failure
and requests diagnosis only. Main owns read-only process/Telegram/log probes;
code tracing follows only the observed error. Population: Bria v2 since the
10:36 restart, especially latest owner interaction. Acceptance: distinguish
process crash, transport failure, blocked provider and callback/controller
failure using current physical evidence. No restart, deploy, outgoing message,
config or state mutation authorized by this follow-up. No parallel worker is
needed for the initial cheap runtime probe; code tracing will be split only
if independent failure paths remain. Ranked hypotheses: crashed service
(PID/runs change), Telegram connectivity (getMe/log transport failures),
blocked interaction/session (live process plus pending/rejected event).
Status: in progress. Initial probe: same PID 54746, runs=14, state=running;
stdout/stderr unchanged. This rejects a completed process crash/restart, not
an interaction failure or deadlock.
Artem clarified A16: pagination appeared unresponsive; opening the menu and
returning to the session restored updates. Main owns exact runtime/log/registry
evidence; independent read-only worker owns code trace of pagination tokens and
card-refresh ordering in telegrampipeline/telegramflow/callback registry. Same
diagnosis-only contract, no source edits or external writes. Stop at a precise
rejection/race hypothesis and distinguishing evidence; main synthesizes.

A16 physical evidence: four callback.accept failures at 10:41:32, 10:41:34,
10:41:42 and 10:43:02 Moscow. Each correlated trace has successful ack persist,
ack start, then accept failure and successful ingress completion, with no
callback.plan or controller.callback. Ack records for updates 783531503-506
are confirmed, but no corresponding operation or registry claim was recorded.
Menu callback 783531508 and page_latest 783531509 fully committed at 10:43:06
and 10:43:16 respectively. Exact error text is deliberately redacted; observer
collapses all such errors to telegram_flow, so stale/replayed/invalid cannot
be distinguished from these logs alone. At 10:46:12 provider submit finished
successfully, active session became ready, state commit at 10:46:13. Service
remains the same PID/runs, proving this was not a service crash.

A16 conclusion: diagnosis complete, no runtime/code changes made. Main verified
the independent code trace: flow.go acknowledges before accept; recoverable
token/stale/replay errors are silently skipped. Telegram edit precedes
finalizePrepared/BindPresentation, leaving a visible-but-unbound keyboard
window. Direct prompt refresh calls sendPrepared outside the durable delivery
mutex; commitCard writes the prepared page without a generation guard. These
are concrete code risks, not proof of which caused the four historical rejects.
Retired ordinary same-carrier tokens survive refresh after registry reopen, so
refresh alone does not imply staleness. Precise historical subtype is lost:
no rejected callback payload is persisted and all error subtypes collapse to
telegram_flow before redaction. Leading candidate is presentation/token
staleness; replay remains possible. Recommended follow-up (not implemented):
safe typed rejection reason, per-carrier ordering/generation protection, and
tests for navigation during delayed refresh and visible-before-bind delivery.
Earlier restart acceptance covered process/getMe, not live pagination.

| ID | Owner / exclusive write scope | Acceptance | State |
| --- | --- | --- | --- |
| A1 | Controller worker: internal/telegramcontroller; new controller helper package | Three race reproductions green repeatedly; controller <=5500 lines | done; independent 20-repeat checks |
| A2 | Screen worker: internal/screen, internal/screenproduction; new native render/cache packages | Screen <=750; composition <=200; behavior tests | done; independent race checks |
| A3 | Media worker: internal/mediaproduction, internal/p4runtimecomposition; new document adapter package | No telegram/controller import in media production; <=800; integration tests | done; reviewed custody behavior |
| A4 | Settings worker: internal/settings, internal/settingscomposition; new preference/codec package | Settings <=800; composition <=200; persisted toggle compatibility | done; migrations v1-v5 verified |
| A5 | Storage/transport worker: internal/storage, internal/telegram; new pure history/format packages | Storage <=1900; telegram <=1750; persistence/format tests | done; atomic commit boundary retained |
| A6 | Bridge worker: internal/telegrambridge; new callback helper package | Bridge <=1550; callback compatibility tests | done; signed callback consumer tests |
| A7 | Main: internal/nativeadapter, new internal/nativecapture, scripts | Remove screen dependency; adapter <=850; architecture green | done; real isolated tmux race tests |
| A8 | Main integration: docs, whole-tree verification/build | Full tests/race, policy, architecture, format, vet, physical binaries | done; full gate also passed after sanitization |
| A9 | Main: read-only Git/service target discovery, then gated release | Exact commit/remote branch reread; restart health/process evidence | done; remote SHA, CI and live PID 54746 verified |
| A10 | Former settings worker: cmd/bria test files only | Stop fake Codex recursively executing whole test suite; exact helper regression | done; bounded red/green + whole cmd race |
| A11 | Controller worker: internal/integration/telegram_callback_acceptance_test.go | Async callback HTTP fake race reproduced then repeat green | done; whole integration race count=10 |
| A12 | Main: Makefile and push CI workflows | Provision pinned dependencies before offline checks; native test prerequisites | done; both push workflows successful |
| A13 | Main: approval docs/fixtures only | No internal work-source identities or business values in public Git payload; raw receipts retained locally | done; sanitized full gate passed |
| A14 | CI worker: internal/sessionruntime orphan cleanup, its starter.go call/import; new internal/orphanresume | Linux runtime <=1850 with unchanged cleanup behavior | done; real Linux process tests and race passed in CI |
| A15 | Main: internal/nativeterminal, scripts architecture checker/tests | Linux terminal <=510; architecture inspects all supported GOOS/GOARCH | done; full local and Linux CI gates passed |

Workers must not edit scripts or shared docs. Public API aliases/adapters preserve
consumer compatibility; report any required cross-owner edits to main. Main
registers new packages with justified bounded budgets, never raises old limits.
New packages must have a real responsibility, not merely relocate random lines.
Stop each zone at passing scoped checks and evidence handoff. No external
queries/live writes for workers. Final full verification only after integration.
Existing LUNA_APPROVAL_TODO and stabilization backlog remain preserved.

CI continuation uses the same contract and approved publication sequence. Main
owns integration, terminal cleanup simplification, checker, docs and release;
the independent worker owns only orphan-resume extraction and its tests. No
cross-owner edits, no live process operations by the worker. Stop at scoped
behavior checks and unchanged Linux package limits; then main runs full gates.
One worker plus main covers both independent code areas without shared writes.

Initial push receipt: remote main = 5d0a8b6ffe0efb4a1fbcf4bf5ecd0e96c502c8f9.
Platform run 34198434479 succeeded (8 jobs). Stage1 run 34198434480 failed:
Linux sessionruntime=1947/1850 and nativeterminal=512/510. No deploy occurred.
Hypotheses: platform-selected GoFiles differ (reproduce with Linux go list),
Go-version counting drift (compare same files locally), stale remote source
(compare SHA). Exact remote SHA matched; platform-specific files are present.
Add all supported platform graph checks so host-only acceptance cannot recur.

Confirmed: the same local Go toolchain reproduces both exact Linux counts when
go list selects Linux files; no version/counting drift is needed to explain CI.
New checker regression first failed all five cases (four target-specific caps
and a Linux/arm64-only forbidden import), then passed after checking all shipped
targets. The enhanced checker reproduced the original Linux CI errors locally.
Terminal cleanup now parses each discovered PID once and skips already-owned
identities before reading metadata; pinned-descriptor and parentage rechecks
remain intact. Linux terminal size is 507/510. Fresh macOS isolated-tmux race
tests passed; Linux/arm64 test executable compiles. Linux runtime execution of
the changed cleanup still requires the next CI receipt.

CI-fix integration: orphan-resume cleanup is now a stdlib-only platform module
(112/150 Linux lines); sessionruntime is 1842/1850 on Linux and macOS. Existing
Start request validation requires a prior binding before the direct resume
cleanup call. Removed only obsolete private wrappers, not a public API. Main
reviewed the exact selection/termination diff and Linux test child ownership.
Focused race/vet checks and Linux test cross-compilation passed. Canonical
make check-full passed again with all-platform architecture enforcement and
physical executable-trio acceptance. Next: commit/push scoped CI correction,
read actual Linux process-test and full push CI receipts, then approved deploy.

## Final live receipt - 2026-09-08, 10:37 Europe/Moscow

All A1-A15 obligations are closed for this bounded fix/release. Earlier pending
statements below are retained as chronological evidence, not current blockers.

- Published source: main and origin main both
  `24921b7ebcab9b23a16683253f9a4461c84b01ac`, after initial fix `5d0a8b6`.
- [Stage 1 / full Linux gate](https://github.com/Time4Mind/bria/actions/runs/34199753600):
  SUCCESS. Logs confirm nativeterminal, orphanresume and sessionruntime in plain
  and race suites, all-target architecture, and executable-trio acceptance.
- [Platform matrix](https://github.com/Time4Mind/bria/actions/runs/34199753590):
  SUCCESS, all eight jobs. WSL is scaffold-only, not real WSL runtime acceptance.
- Installed only the verified macOS arm64 trio into the exact approved release
  `20260908-architecture-races`, switched current atomically, then restarted
  only `gui/501/com.time4mind.bria.v2` once. Old release remains available.
- LaunchAgent state `running`, runs 13 -> 14, PID 38207 -> 54746. The live
  process executable resolves to the new release; PID stable for over 71s.
  Read-only flock probe confirms the exclusive instance lock is held by it.
- Version `bria 20260908-architecture-races`; installed/source SHA256 identical:
  bria `4dcea6ab49a52bc9c1b89d1acaed16a472db921628985c8bc59185ec103cca8b`,
  codex adapter `92ba63c79ec5ad9be48827ebbbc61f6d86217e607fa8f44ff285ee4ef2f70ffe`,
  claude adapter `4b1046f80b213726c53c566919b3b309d6e3124f79c8c68657938120f46470ed`.
- Configuration checksum unchanged; settings read-only and isolated fresh
  session-state validation passed. No manual config, credential or state edits.
  Two post-restart Telegram getMe checks passed. No new critical/service error
  events since startup; stdout/stderr log stayed at its pre-restart 19042 bytes.
- No manual Telegram message or new provider task was sent. A new interactive
  Telegram/provider end-to-end scenario is not claimed by this restart receipt.
- This post-deployment journal update is local-only. Published code and tests
  are exactly the CI-verified SHA above; no extra external write was performed
  after completion of the approved Git sequence.

## A21 - card spacing and dedicated final pages (2026-09-08)

Immutable contract: implement the latest user formatting request in current Bria.
Exactly one visible newline follows the header divider in Telegram text. Every
nonempty final starts on its own page, regardless of length; continuation pages
contain that final only, and following prompts/events start another page.
Sources: latest user request, current repository, local behavioral tests.
Unit: rendered card/page; all final lengths, adjacent finals, empty history,
technical-action filtering and page limits. No period-dependent data.
Authorized writes: source/tests/docs/local builds only. No push, deploy, restart,
live state repair or outgoing Telegram messages under A21.

Coverage/ownership: main owns telegramcontroller, cardtranscript, integration
final_pages_acceptance_test.go, telegrampromptcomposition and this ledger;
spacing worker owns telegramcompletioncomposition, telegramformat and the
disjoint integration/card_spacing_acceptance_test.go (controller header changes
requested from main); independent
reviewer is read-only after integration. No overlapping writes. These two
behavior slices exhaust useful parallel implementation; no external research.
Acceptance: demonstrated behavioral RED/GREEN, real Markdown wire text, typed
final page boundaries through controller, neighboring tests and full local gate.
Live visual acceptance remains pending separately authorized deployment.

| ID | Obligation | Status | Evidence / remaining boundary |
| --- | --- | --- | --- |
| A21.1 | One newline after header divider | locally verified | Real controller/runtime/flow/bridge plus prompt/completion JSON wire: 9 RED cases then 18 GREEN checks, race PASS; plain entities and Rich hardbreak |
| A21.2 | Dedicated short/long final pages | locally verified | Public controller RED: 1/3 pages; GREEN with isolated finals, UTF-8, fenced code, empty blocks, latest window; real store reopen and technical-toggle race PASS |
| A21.3 | Integration, review and full local gate | locally verified | Independent review approve after correction; canonical make check-full PASS, executable trio accepted |
| A21.4 | Live acceptance | not authorized | No service or Telegram writes |

A21 evidence so far: rendering previously erased `final` identity into strings
before byte-budget pagination. `RenderBlocks` now preserves types; pagination
lives in cardtranscript (existing 500-line cap unchanged) and controller exposes
the same page shape. Durable history/schema and A19 turn ordering are unchanged.
One existing filter test assumed final and commentary share page 1; now it checks
filtered intermediate page and separate final page, including legacy last view.
Initial placement of the real-store test in controller was rejected by the
architecture gate; moved to integration without allowing the forbidden edge.
Spacing worker reproduced 6 extra-newline completion failures and 9 plain-wire
hardbreak-space/offset failures; fixes and scoped/race consumers pass. Main
integration of controller hardbreak and prompt-status refresh is complete.
Independent review reproduced an inherited line-split repacking defect: one
5847-byte final became 5859 bytes because fragments acquired inter-event
separators. Added exact-content RED/GREEN regression and flush each final
fragment onto its own continuation page. Ordinary block packing is unchanged.

Final A21 local acceptance (2026-09-08):

```sh
env TMPDIR=/private/tmp GOMODCACHE=/Users/a-s-nosko/go/pkg/mod \
  VERSION=20260908-card-layout make check-full
```

Exit 0, `Bria executable trio acceptance: OK`: policy, formatting, secret scan,
architecture for supported targets, full tests/race/vet, operational packaging
and physical macOS arm64 trio. `bin/bria --version` reports
`bria 20260908-card-layout`; SHA256 of bin/bria:
`19b306287cfd06ee4acc86151e1a1b59199ba2c50b06b64f7879be5da1de44ea`.
Two adapters retain A20 hashes. Dedicated-page projection/reopen tests also
passed ten race repetitions; new actual JSON wire integration passed race.
Independent review approved the final frozen source/test set after the
line-split correction. Existing A17-A20 changes were preserved. No push.
Live current link reread still selects `20260908-card-order`; A21 was not
installed, no service restart or Telegram write. Visual Rich rendering remains
unverified beyond the explicit Markdown hardbreak in the actual wire payload.

## A24 - voice status line and disappeared session diagnosis (2026-09-08)

Contract: fix/verify voice-recognition status starts on the line after the header
divider. Diagnose, without live repair, why the new workdir session disappeared
from buttons but remains on the visible card, and where bulky unexpected card text
originated. Current repo and bounded read-only live logs/state are the sources;
installed reread is 20260908-card-order. Initial incident 13:40:45-13:41:47 UTC,
16:40:45-16:41:47 MSK: session startup ready followed by provider error. Unit: exact
voice-status projection and exact incident session/card/provider lifecycle.
Writes: local voice code/tests if needed and diagnostic docs/fixtures only.
Session/text fixes require new authority after diagnosis; no live state/card writes,
push/install/restart/messages/provider control/Legacy access/memory writes. Preserve
A17-A23 dirty work. Prior layout fixes may already satisfy voice locally but are not
installed; prove this distinction before adding another fix.

Coverage before fan-out: main owns live state/log evidence, contract/docs and
synthesis. Voice worker owns a new targeted regression test and only the bounded
voice-layout seam if still broken (report before source edit). Provider/UI worker
read-only traces recovery/button filtering and error-text provenance. No overlapping
source ownership; main integrates. Stop when exact lifecycle/card mismatch and
text origin are traced, not at a generic provider_error. Bounded latest incident
only; do not dump prompts, credentials or complete raw stderr.

| ID | Obligation | Status | Acceptance |
| --- | --- | --- | --- |
| A24.1 | Voice status line after divider | locally verified; deployment pending authority | new real-controller pending-voice to Rich HTTP regression; A21 hardbreak already present; no production change |
| A24.2 | Session missing from buttons yet visible | diagnosis complete; fix not authorized | exact state/logs and public Reader replay reproduce fatal oversized native record; recovery excluded from buttons but selected card retained |
| A24.3 | Bulky unexpected card text | diagnosis complete; presentation fix not authorized | native metadata projection exactly equals stored history item 9; technical actions enabled |
| A24.4 | Validation and handoff | local acceptance complete; live unchanged | independent source investigation, main replay and 10 race repetitions; installed release remains card-order |

A24 evidence (2026-09-08 UTC, MSK = UTC+3): new workdir session
`69e9b6a2-e7dc-42c5-a431-9c4818a92515`, native binding
`01a0813f-ece0-78b2-9312-be51f3f88aba`, became ready at 13:40:45.953.
At 13:41:46.872 native `event_msg.item_completed` recorded a successful
CommandExecution (exit 0, stdout 1,048,609 bytes). The entire compact JSON record
is 2,319,315 bytes, exceeding Reader's 1,048,576-byte default before event-type
filtering. At 13:41:47.000 Bria reports generic provider_error; at .002 the
session is awaiting_recovery with recovery_target=running. No final/complete
exists in the native transcript. The accepted receipt remains unknown.

Main read-only replay through public nativetranscript.Open/Poll on this exact
bound transcript returns ErrLimit with default settings. Diagnostic-only control
with MaxLineBytes=3 MiB reads 13 events without error. No production limit changed.
The following 42,232-byte tool output was NOT the oversized record: the earlier
42 KB hypothesis is disproved. Reader errors abort nativeadapter.poll/Run and its
deferred terminal Close; this is a Bria reader failure, not evidence that Luna
independently failed. Supervisor then records recovery. Installed and local Codex
adapter SHA256 are identical:
`92ba63c79ec5ad9be48827ebbbc61f6d86217e607fa8f44ff285ee4ef2f70ffe`.

Persisted active_session remains the same new session. Selectable excludes
awaiting_recovery, while semanticCard loads its ID without that filter. Thus only
the ccbot ready session remains selectable on bria-local, though the new workdir
card remains foreground. No deletion occurred. Physical card send/commit at
13:41:47.091-.306 succeeded on page 3/3. This proves delivery processing, not a
healthy provider or current Telegram visual appearance.

History has 11 rows (prompt, commentary, nine tool rows), no final. Persisted
show_technical_actions=true. The 6,122-character history item 9 is tool metadata
containing a 6,000-byte prefix of native tool output. Replaying native metadata
through cardtranscript.EncodeTool matches this stored item byte-for-byte, without
printing its private contents. Raw content-block JSON / escaped text in tool
results, rather than an assistant final, explains the bulky technical content.
Settings and existing history were not edited.

Voice regression: internal/telegrampromptcomposition/voice_spacing_test.go drives
a real controller with recognition held pending through projection, Deliverer,
Presenter and Sender to captured HTTP. Old-header negative control has a soft
newline; current Rich Markdown carries exactly two spaces + newline before the
voice marker, without a blank line. Main independently reran 10 race repetitions;
worker checked five adjacent packages, existing integration spacing acceptance,
architecture/formatting and diff whitespace. No production change or new build;
A23 full gate remains evidence for unchanged production, not a new live rollout.
Actual Telegram rendering and installed service are not changed/tested by this
offline probe. Local diagnostic helper is ignored .cache/a24-probe; no transcript
copy or private payload was stored in tracked files.

Next scope requiring authorization: robust bounded handling of oversized native
records (not merely an unbounded cap increase), explicit safe failure telemetry
across adapter exit, coherent recovery visibility/selection, and readable tool
content-block formatting. Existing schema 2 logs lack the exact reader-failure
class/size, so logs alone were insufficient; transcript replay supplied causality.
Suggested telemetry: native_transcript_record_too_large, correlation, offset,
observed/limit bytes and receipt state, without payload/raw stderr. Installation,
restart and affected-session recovery remain separately gated; do not replay an
accepted unknown prompt automatically.

## A23 - archive fallback selection and observable switching (2026-09-08)

Immutable contract: diagnose and fix archive/close returning to the closed card
instead of another selectable session; make the switch decision and delivery
visible in safe logs. Sources current repo and read-only runtime state/logs.
Incident 2026-09-08 12:54:25-29 UTC (15:54:25-29 MSK). Unit: selection and visible
card after immediate/scheduled archive, remaining same-node selectable session or
none; preserve newer user selection and other nodes. No unrelated lifecycle rewrite.
Allowed local source/tests/docs/build and synthetic temporary fixtures. Excluded
live state/config/card mutations, provider control, messages, push/deploy/restart,
Legacy access and memory writes. Existing A17-A22 changes remain owned/preserved.
Acceptance: public-seam RED/GREEN of observed path, physical JSONL chain and safe
fields, state/wire agreement, ordering/race checks, independent review, full gate
and physical build. Installed/live acceptance is separate and not authorized.

Coverage before fan-out: lifecycle worker owns telegramcontroller source/tests
only, initially diagnosis/repro then bounded fix after evidence; main owns live
read-only investigation and telegramflow/consumer wiring if needed. Logging worker
initially read-only owns gap/schema design for telegramtrace/callbackdiagnostic/
observability/safelog; implementation ownership fixed after shared event contract.
Neither edits the other's packages. Main owns docs/policy/integration, is the sole
integration owner. Independent review follows source freeze. No external broad
search; stop diagnosis when archive outcome, fallback decision, persistence and
projected target are established; cheap local repro before broader fixes.

Implementation ownership: main also owns app completion seam, composition and
policy. Logging worker owns new context/time-only controllertelemetry, trace,
observability and safelog. Third worker owns only a new telegramflow integration
test, validating callback-versus-refresh delivery ordering and physical wire.
Dependencies: frozen typed telemetry contract; app completion reports only async
results, after durable lifecycle write and outside closer lock, preserving context
values without cancellation. No additional external targets or writes.
Independent reviewer owns read-only review of frozen app/telemetry/composition
changes, then controller/consumer changes after freeze. Main alone integrates.
Lifecycle worker also owns telegramnodes close-resolution extraction and tests;
real-store controller tests moved to cmd/bria to preserve lower package boundaries.
Main owns the atomic storage SetNodeLastActiveSession port and its test: background
fallback must never navigate foreground, including intermediate durable state.

| ID | Obligation | Status | Acceptance |
| --- | --- | --- | --- |
| A23.1 | Reconstruct incident and cause | verified locally | bounded live logs/state + matching public RED/GREEN |
| A23.2 | Make switch stages observable | verified locally | real controller JSONL chain/HMAC matches card delivery; schema 3 |
| A23.3 | Correct fallback/card behavior | verified locally | immediate/deferred, busy, none, provider failure, newer selection, background-node persistence |
| A23.4 | End-to-end local validation | verified locally | final independent approve; full check/race/trio PASS; physical 20260908-archive-switch |

Initial ranked hypotheses: (1) interactive close returns scheduled and its later
archive completion never refreshes fallback; (2) fallback changes but stale close
projection overwrites it; (3) scope persistence/recent-list exclusion loses the
remaining candidate. Observed close callback edits the original session at
12:54:27. Its archive state_changed_at=12:54:26.919188Z is the logical timestamp
captured before provider Abort, not a measurement of the archive disk write. Manual
select_session at12:54:28.874733Z edits the other session at12:54:29.081886Z.
Current logs show the bad projection and successful Telegram edit/commit, but no
explicit archive-result/fallback-decision/selection-persistence trace. Current
state after that manual selection cannot prove the earlier automatic selection.

Confirmed mechanism: app.BeginClose previously spawned Close and discarded its
result/error; the interactive controller returned a scheduled closing card and
did not complete the normal fallback-selection path. An already completed archive
could also be projected using the stale controller active session. Busy-turn close
had a separate worker completion path and is retained.

Implementation: BeginCloseWithCompletion reports asynchronous durable outcomes
outside the lifecycle lock, preserving operation context without callback
cancellation. A ready session's durable Closing transition now detaches it from
active selection before preparing the close callback response. The response edits
the clicked carrier with the remaining selectable session or the sessions menu;
no old prepared closing card remains for a late send to overwrite the fallback.
This early selection is not an archive claim: provider failure remains
AwaitingRecovery and emits a failed archive outcome. Clarification announced to
the owner during implementation: the same immediate UI detachment applies to
ClosingAfterWork, which is already excluded from selectable sessions. The running
request continues in the background without interruption; its existing worker
still performs physical closure only after work finishes. Empty selection uses
the original close callback carrier to show the sessions menu, so no unobserved
later empty-fallback notification is needed. Busy close retains the initiating
operation for its eventual lifecycle/fallback diagnostics within the process.

Intermediate evidence: app public RED missing-completion failure -> GREEN for
archive/provider failure/cancellation; real storage/controller+signed callback+
prompt delivery+flow sender integration initially failed 4/5 cases, now 7/7 PASS
(same/different/missing fallback carrier, none, delayed delivery, fast archive with
and without fallback). Disk state reopened and exact clicked carrier checked.
Real controller JSONL joins initiating close, fallback, persist, project, edit,
transport and state.commit; opaque target/card session refs agree. Main also ran
10 race repetitions of the consumer integration successfully. Additional probes:
newer manual selection survives delayed close (10 race repetitions); background
node cleanup preserves foreground atomically; missing persistence is not logged as
success, and rejected writes remain retryable. Four real busy-worker scenarios
(running/stopping, with/without fallback), each repeated three times under race,
verify immediate callback projection, Abort=0 before work finishes, exact one
provider close afterward, durable archive and original close operation retention.
Repeated BeginClose on ClosingAfterWork had a further public RED: it interrupted
the running provider. The guard now returns the existing schedule without starting
another close goroutine; GREEN. Lifecycle diagnostics moved to sessioncloseflow,
node fallback resolution to telegramnodes; old duplicate Remove delegates to the
same resolver. Existing package caps remain unchanged. Lower-layer independent
review approved; final integrated review/full gate/build still running. The first
full attempt stopped at formatting; gofmt fixed the single composition alignment
and the complete gate was restarted. That gate passed, including executable trio,
but integrated review identified two additional untested edges, so it is not the
final acceptance: concurrent BeginClose could cross an unlock/reload window and
interrupt busy work; a failed repeated close could delete another operation's
pending correlation. Both now have public RED/GREEN: unique owned correlation
leases use CompareAndDelete; interactive lifecycle read/decision/write stay under
one mutex via closeCurrent. A 32-caller, 200-round stress test previously observed
31 premature Aborts and now passes, including three independent race repetitions.
Integrated independent review approved the final app/flow hashes fbfcb806/87df639a;
main reread exact matching SHA-256 values. Final fresh
`VERSION=20260908-archive-switch make check-full` completed exit 0, including all
tests, race, vet, architecture, secret scan, packaging and executable trio acceptance.
`bin/bria version` reports `bria 20260908-archive-switch`; binary SHA-256:
`9f227b2b868cc9fca1f18a563702813003e60e1b824098f7a06ededd67ffa156`.
Adapter SHA-256 remained `92ba63c79ec5ad9be48827ebbbc61f6d86217e607fa8f44ff285ee4ef2f70ffe`
(Codex), `4b1046f80b213726c53c566919b3b309d6e3124f79c8c68657938120f46470ed` (Claude).
All A23 local obligations reconciled. No live writes, install, push or restart
occurred; installed symlink reread still resolves to 20260908-card-order. Real
Telegram client repetition and live schema-3 records remain unverified until a
separately authorized installation. Prior A21/A22 changes remain included locally;
notification page-plan format-v2 rollback constraints remain in force.

## A22 - table rendering parity with historical Bria Legacy (2026-09-08)

Contract revision 3, 2026-09-08 Europe/Moscow: owner explicitly approved saved
page plans (A22.6 option 2). New notifications persist immutable final Rich pages
before sending; old receipt/unknown operations retain exact legacy parts and IDs.
Resume reads saved pages; bind chat/session/kind/text identity and reject changed
input or corruption. Preserve full UTF-8 body, repeated table headers/code fences,
final 4096-byte payload limit including prefix/normalization, confirmed/no-retry
unknown semantics, and A21. Concurrency and crash after send-before-receipt must
not produce automatic duplicates. Local source/tests/docs/build and synthetic
temp files only; no live state/config/messages/push/deploy/restart.
Acceptance: RED/GREEN through actual file store + HTTP request boundary, restart,
old v1 confirmed/unknown/empty operations, input conflict, concurrent claims,
failure before persistence, independent review and fresh full gate/physical build.

Revision 3 coverage before fan-out: main owns notifier.go, delivery.go and new
notification delivery integration tests, policy, docs and final synthesis. Pure
planner worker owns new internal/notificationplan only (no I/O), providing final
Rich pages with explicit byte budget. Storage worker owns new
internal/notificationstate and existing telegramnotify file_part_receipt_store.go
and filelock files only, extracting durable state below sender and retaining its
old public facade; owns new storage tests. Reviewer is read-only, starts after
interfaces freeze. Dependencies: planner types -> storage and main; no overlapping
files. No external search/live tests; stop each zone when assigned behavioral
contract and scoped checks pass. Existing package caps stay unchanged; new pure
planner cap 250, durable notification state cap 650, explicit downward edges.
Shared API: notificationplan.Plan{Version int, InputHash string, Pages []string};
Rich(prefix,text string,limit int)([]string,error). State package owns DeliveryState
and PartReceipt (sender aliases preserve existing API). Store GetOrCreatePlan(ctx,
operationID,inputHash string,richPages,legacyPages []string)(Plan,error) atomically
chooses old/new plan under receipt lock. ClaimPart(ctx,operationID,partID)
(bool,error) persists unknown before external write; false must never send.
ReleasePart(ctx,operationID,partID) only clears claim after definitive rejection.
Old part-store-only implementations keep the historical compatibility path.
Notify with stable OperationID and durable plan store uses Deliver; transient
Notify without stable identity uses same Rich paginator but cannot promise resume.

Revision 3 implementation: public store facade retained; durable storage moved to
notificationstate below notifier to keep original package caps. LoadPlan was added
to the shared API so resumption does not even invoke the current paginator.
New plan pages include final prefix/normalization; old v1 raw-part payloads remain
exact, including their historical post-normalization size exception. Atomic claims
are saved as unknown before transport; definitive rejection releases the claim,
confirmed/ambiguous outcomes retain their proper fence. Operational compatibility
and limits: [NOTIFICATION_PAGE_PLANS.md](NOTIFICATION_PAGE_PLANS.md).

Revision 3 reviewer corrections: HTTP 408 was traced to ErrDeliveryUnknown despite
its 4xx status. Regression first reproduced failed instead of unknown; transport
uncertainty now takes precedence and the 408/reopen one-attempt test passes.
Loss of an individual plans entry could previously masquerade as legacy state;
the storage owner added whole-v2-state checksum verification in addition to each
plan checksum. Main consumer test deletes only the plan, changes input, and asserts
zero HTTP plus byte-identical corrupt file after rejection. Final full gate must
run after these fixes; the earlier green pass is not acceptance of final revision.

Revision 3 final acceptance, all source frozen after reviewer corrections:

```sh
env TMPDIR=/private/tmp GOMODCACHE=/Users/a-s-nosko/go/pkg/mod \
  VERSION=20260908-saved-page-plans make check-full
```

Exit 0, terminal `Bria executable trio acceptance: OK`. Full plain/race, vet,
policy/secret scan, all supported-target architecture, operational packaging and
physical trio passed. Independent review approved both fixes; main reread actual
code, tests and file-state behavior. Separate fresh notifier/state race and
durable/completion/singlemachine/integration/controller race passed. New package
caps: notificationplan 250, notificationstate 650 (620 production lines); all old
caps and required AGENTS clauses unchanged.

Physical version: `bria 20260908-saved-page-plans`.
SHA-256:

- bria: `34390a85a4e97a6c46d90ab787307436850a43ebfb579c8cab3558ff36c4d354`
- codex adapter: `92ba63c79ec5ad9be48827ebbbc61f6d86217e607fa8f44ff285ee4ef2f70ffe`
- claude adapter: `4b1046f80b213726c53c566919b3b309d6e3124f79c8c68657938120f46470ed`

All current approved local A22 obligations are verified, including A22.6; agents
closed. Persisted live state/config were not edited. New code will migrate lazily
on plan creation after a separately authorized installation. No outgoing message,
commit/push/deploy/restart or Legacy edit occurred. Installed current reread remains
`20260908-card-order`. User-visible Telegram rendering and installed runtime remain
unverified for this build. Future rollback must keep a plan-capable binary: do not
delete plans, downgrade version fields or restore stale receipts to force startup.
The former A22.6 decision-pending notes below describe revision 2, superseded here.

Contract revision 2, explicit latest user addition: use Rich Markdown for ALL
outgoing Bria text messages, not just table/card paths, for consistent rendering.
Population now includes cards, menus, statuses, background/error/auth/recovery
text notifications and their send/edit paths. Files/photos keep their existing
binary delivery contract; no text fallback after uncertain Rich sends.
All earlier A22 and A21 rendering/data-preservation obligations remain. Main
owns universal transport routing and audits every production SendMessage call;
workers retain disjoint renderer/pagination ownership and receive this revision.
Notifier worker owns internal/telegramnotify source/tests only: migrate its two
direct text-send paths and verify receipts/unknown-send behavior. Main owns
telegrambridge source/tests, integration and policy. Independent review covers
all three transport entry points. Stop call-site audit when no production text
sender outside approved Rich transport remains (low-level client plain API may
remain for API compatibility but must have no Bria product sender).
No new external write/deploy/restart/push authorization is implied.

Immutable contract: investigate the reported unrendered table in current Bria,
delegate historical renderer inspection in /Users/a-s-nosko/pet_projects/bria-legacy,
compare it against current repository requirements and implement compatible
parity. Any material requirements conflict must first be presented to Artem in
a comparison table and explicitly resolved; no silent requirements changes.
Sources: latest user request, current source/docs/runtime metadata, historical
Legacy code only for the explicitly requested comparison. Unit: table in a
session card/final across send/edit and page boundaries. Current local runtime
and latest relevant delivery logs; timestamps UTC, report MSK if needed.
Allowed writes: local source/tests/docs/build and synthetic local fixtures.
Excluded: Legacy edits, secrets, push, deploy, restart and outgoing messages.
Acceptance: evidence-backed parity/conflict decision; if compatible, behavioral
RED/GREEN at rendering+transport boundary, preservation of A21 requirements,
independent review and full local gate. External visual proof requires separate
authorization; do not equate wire receipt with observed Telegram rendering.

Coverage and ownership before fan-out: Legacy agent read-only owns historical
renderer/callers/tests discovery (bounded to relevant files, no full history);
main owns current renderer/runtime/requirements diagnosis, comparison synthesis,
ledger and any integration. Implementation ownership assigned after conflict
gate; no parallel source writes before semantics are fixed. Stop historical
search when render decision, outputs, fallbacks and tests are evidenced.
Main is sole integration owner. No web or broad external search needed.

| ID | Obligation | Status | Acceptance / evidence |
| --- | --- | --- | --- |
| A22.1 | Agent inspects Legacy rendering | verified | Historical projector_card.go, telegrambot/rich.go, screen.go, rich_payload.go; main reread |
| A22.2 | Current cause and requirement comparison | verified | Local in-memory HTTP probe: flag=false sendMessage literal; flag=true sendRichMessage native table. Core Rich rendering compatible |
| A22.3 | Implement compatible parity | locally verified | Bridge/card-mode and table pagination RED/GREEN; long-fence byte-budget finding fixed and independently approved; integration/cardtranscript fresh race PASS |
| A22.4 | Final validation | locally verified; live pending | Final make check-full PASS, executable trio acceptance OK; physical version/hash reread; live not authorized |
| A22.5 | Universal Rich text send/edit | locally verified | All three bridge boundaries and both notifier boundaries use normalized Rich; actual JSON/race, no retry after unknown/reopen |
| A22.6 | Saved notification page plans | locally verified | Revision 3 saved Rich pages, v1/empty-operation compatibility, pre-send claims, exact resume, process/crash/race tests, final make check-full and physical build PASS; live not authorized |

Initial diagnosis, ranked falsifiable hypotheses:
1. Final takes entity-text transport, bypassing existing Rich table rendering.
   Prediction: unflagged final uses sendMessage with pipe-delimited literal text.
2. Byte pagination removes table header/separator on continuation pages.
   Prediction: only oversized tables fail, including when Rich is selected.
3. Telegram rejects Rich payload or current client cannot display it.
   Prediction: explicit rich request/error or client-only failure after valid wire.
Live read-only probe: installed current still `20260908-card-order`; latest
affected card has 59 entries and page 15/15. Final has 859 characters / 1262 UTF-8
bytes, two table separators and 14 pipe lines, no fences. Last transport.send
and state.commit completed at 2026-09-08T11:28:55Z. No private table values were
copied to repository. This rules out oversized final splitting for this case;
logs alone do not identify rendering mode or prove visible client output.
Pre-fix source: bridge selects Rich only for explicit RichMarkdown or Screen
source/session pairing; completion supplies neither. Plain Markdown formatter
handles bold/code, not tables. Existing Rich normalization already wraps cells
in sub tags. Historical comparison confirms the same renderer representation.

Conflict gate result: native Rich Markdown tables with compact sub cells,
independent of Screen, are compatible with current UX and A21. No product
requirement is changed. Historical delivery fallback/timeout retry and bounded
plain-text truncation are NOT table rendering and are outside this port: retain
current one-attempt delivery safety, final isolation and history preservation.
No claim is made that Legacy's raw blank-before-table changes the visible A21
spacing; actual Rich client layout remains a separate live acceptance boundary.

Implementation coverage after gate: main owns telegrambridge, telegramflow
card render mode and integration/completion wire regressions, ledger and final
gate. Renderer worker owns
internal/telegramrich and internal/telegram/rich.go + rich_test.go only;
retain literal fenced/code examples. The initial table-detection public API was
removed under revision 2 because unconditional Rich has no detection caller. Pagination
worker owns internal/cardtranscript/pagination.go and new table-pagination
source/tests only. Historical card_markdown_pagination.go confirms repeated
headers/separators and atomic rows on continuation pages; oversized individual
rows fall back to bounded text without truncation. Port that behavior within
the unchanged cardtranscript cap, preserving A21.
No source overlap; review is read-only once integration is stable.

Final gate fixture ownership addition: notifier worker, after freezing notifier,
owns only cmd/bria/main_test.go and telegram_flow_integration_test.go to migrate
old sendMessage expectations to actual Rich JSON. The first full gate stalled in
the CLI test fixture's old method assertion, not the running service. Main stopped
only that invocation's bria.test PID 98555 with SIGQUIT; no production process was
stopped. The full gate must pass again after fixture/pager source freeze.

The fixture migration is green at the actual CLI composition boundary: plain
cmd/bria and targeted race scenarios preserve quarantine, queue limits, signed
callbacks, exact Rich JSON and no replay. Background fixture failures now return
errors/cancel bounded test contexts instead of abandoning a goroutine with Fatal.
No production runtime cancellation or timeout policy changed.
The independent reviewer found and then approved the long-fence fix: a fixed
80-byte reserve admitted pages over the 3000-byte cap for long markers. New
RED/GREEN cases cover 100/1000-character backtick/tilde fences, language info over
64 characters, impossible wrappers and unclosed fences. Exact wrapper budgeting
preserves all original body bytes; impossible wrappers retain bounded text.
Reviewer original 100-backtick probe now fits 3000/3000/606 bytes. cardtranscript
is 499/500 production lines; telegramrich 88/100 and markdownliteral 60/100.

Architecture adjustment: literal-code detection is shared by renderer and
pagination, not duplicated or squeezed into the renderer's 100-line cap.
Renderer worker additionally owns new internal/markdownliteral, public pure
Lines([]string) []bool boundary, no I/O/internal dependencies, cap100 and literal
fixtures/consumer tests. Both consumers depend downward on it. Main owns exact
policy registration in scripts/check_repo.go; all old caps remain unchanged.

Legacy carrier note (telegrambot/screen.go): rich cards must stay rich during
edits even on text-only pages, not switch according to each page's content.
Current card preparation therefore explicitly selects Rich for all card pages,
including final and close confirmation; Screen remains an independent field.
The initial revision left non-card text unchanged and selected Rich for actual
tables; revision 2 supersedes that conditional routing with universal Rich.
Public regression first reproduced final/refresh Rich=false,
then passed after one explicit card-status flag. A21 wire tests now assert the
same hardbreak/content on Rich card wire, rather than requiring plain transport.

Revision 2 evidence: SendStatus, SendStatusWithKeyboard and
EditStatusWithKeyboard ignore the legacy RichMarkdown hint for transport choice.
Screen controls only optional media. Both Notify and Deliver also normalize and
send Rich. Source audit found no production SendMessage call or SendMessageRequest
construction in Bria product senders. Low-level Telegram API compatibility stays.
The now-unreferenced telegramformat entity converter and its converter-only tests
were removed; the A21 public divider wire regression moved to telegrambridge and
continues to exercise send/edit with the legacy flag both off and on. No stored
config/history/receipts were removed. Architecture orphan detection remains strict.
All old package caps remain unchanged; markdownliteral is the shared pure code
literal detector, not a duplicate renderer dependency.

Notifier compatibility boundary: Notify and Deliver still partition the raw
notification into the same byte/rune-bounded parts as before, then normalize each
part. This preserves existing operation:index:total receipts and unknown markers.
A long table/code block may consequently lose its visual structure across these
notification parts. Reusing the card paginator blindly would change persisted
part meanings and can skip content or resend an unknown part. This inherited
limitation is not a regression in the Rich-only transport change (independent
review confirmed), but is explicitly retained for owner decision: keep stable
old parts, or add versioned/snapshotted page plans for new deliveries while
resuming old deliveries unchanged. No receipt migration or repartitioning has
been implemented without agreement. Card pagination is a separate read-only view
and uses the improved table-aware splitter without changing notification receipts.

Final local acceptance on frozen source, after both pager and CLI-fixture fixes:

```sh
env TMPDIR=/private/tmp GOMODCACHE=/Users/a-s-nosko/go/pkg/mod \
  VERSION=20260908-rich-messages make check-full
```

Exit 0, terminal `Bria executable trio acceptance: OK`. Includes full plain and
race suites, vet, policy/secret scan, all-target architecture, packaging and local
executable trio. Fresh independent integration/cardtranscript race also passed.
Physical `bin/bria --version`: `bria 20260908-rich-messages`.
SHA-256:

- bria: `eb6b315fdc4bf41d29159209a6fd36442f86555ce9c3490e71b4ae5051bcf0fd`
- codex adapter: `92ba63c79ec5ad9be48827ebbbc61f6d86217e607fa8f44ff285ee4ef2f70ffe`
- claude adapter: `4b1046f80b213726c53c566919b3b309d6e3124f79c8c68657938120f46470ed`

All agents closed after direct source/evidence verification. Synthetic temporary
HTTP probe removed; permanent behavioral regressions retained. No private source
table values copied. No commit/push/deploy/restart/live message, history rewrite,
receipt migration or Legacy mutation occurred. Installed symlink reread still
points to `20260908-card-order`. Local Rich JSON and successful HTTP fixtures do
not prove visible Telegram layout or deployment. Remaining owner decisions are
A22.6 (safe long-notification continuation design) and separate live installation/
visual acceptance authority. Neither is silently marked complete.

## Evidence and open boundaries

- Initial review: 3 forbidden edges and 10 oversized packages; 3 test race groups.
- A1-A7 scoped changes integrated. Architecture checker now PASS (all original
  13 categories removed); original controller race cases passed count=10.
- Full plain/race integration uncovered pre-existing fixture failures: recursive
  fake Codex launches in cmd/bria; unsynchronized async callback HTTP fake in
  internal/integration. Stopped only two exact test executable trees, never the
  working service. These directly block the requested full gate and are A10/A11.
- Current origin CI 34130051075 confirms missing module cache with GOPROXY=off.
  A12 prepares declared modules before offline checks, not loosening checks.
- Independent reviewers: screen/settings extraction approved; media/storage and
  controller reviews underway. Main inspects diffs and reruns consumer checks.
- Independent review: controller and screen/settings approved, scoped race suites
  plus 20 controller repetitions passed. Media/storage behavior and atomic writes
  verified. Exported internal TextDocumentPolicy intentionally moved from media
  core into documentproduction; its only production consumer (P4 composition)
  migrated atomically. A backwards alias would recreate the upward dependency;
  no compatibility with nonexistent out-of-module imports is claimed.
- Canonical Makefile now honors caller GOMODCACHE; prepare-deps verified declared
  modules and go.sum; checks remain offline. Build trio runs on macOS arm64;
  configuration check PASS; three other supported cross-build targets PASS.

## Measured package budgets (macOS production files)

| Package | Before | After | Unchanged cap |
| --- | ---: | ---: | ---: |
| telegramcontroller | 5758 | 5499 | 5500 |
| screenproduction | 444 | 135 | 200 |
| screen | 944 | 462 | 750 |
| telegrambridge | 1628 | 1167 | 1550 |
| storage | 1955 | 1890 | 1900 |
| telegram | 1805 | 1735 | 1750 |
| mediaproduction | 837 | 798 | 800 |
| settingscomposition | 237 | 166 | 200 |
| nativeadapter | 867 | 846 | 850 |
| settings | 813 | 710 | 800 |

New responsibilities have explicit bounded policies and reversed-edge rejection
tests. No AGENTS.md clause was removed or weakened. The package caps are guard
rails, not proof that all architecture debt in the repository is eliminated.

## Final-gate diagnostic note

First post-fixture full gate had one 5s readiness timeout in unchanged
TestNestedRuntimeFactoryAdapterCleanupKillsRawGrandchild. Ranked hypotheses:
resource contention (predicts isolated pass), helper/environment recursion
(predicts wrong helper dispatch), protocol regression (predicts repeated exact
failure). Helper has explicit runtime flags and immediate fixture dispatch;
the exact original test passed 10/10 in 4.999s total with no code/timeout edits.
Resource contention remains an inference, not an established cause. Repeat the
unminimized full gate; do not hide this evidence or raise production timeouts.

The repeated canonical command passed end-to-end, including the original nested
test, full race suite, vet, policy, architecture, packaging contracts, executable
trio and CLI acceptance:

```sh
env TMPDIR=/private/tmp GOMODCACHE=/Users/a-s-nosko/go/pkg/mod \
  VERSION=20260908-architecture-races make check-full
```

Read-only release preflight also passed check-config, getMe and validation of an
isolated copy of session state. Remote repository visibility is PUBLIC, so
approval corpus/docs are sanitized for publication. Original work-source
identities and values remain only in ignored `.cache/private-approval-evidence`.
Synthetic fixtures preserve menu shapes and wrapping; no raw work data will be
staged. Final source manifest: ARCHITECTURE_RELEASE_PLAN.md. Push/deployment
confirmation requested separately. Artem's subsequent "Делай" approves both
exact scopes in ARCHITECTURE_RELEASE_PLAN.md; no other targets are authorized.

Final handoff: canonical make check-full PASS again after sanitization, terminal
output `Bria executable trio acceptance: OK`. Production binaries are unchanged
by fixture-only sanitization. All local obligations are complete; only A9 and
its external A12 CI receipt remain open. Next: inspect/stage only manifest files,
commit/push without force,
monitor/reread CI; deploy the verified trio and restart only the recorded v2
LaunchAgent after its separate approval. Revalidate current refs/service/state
before writes because these preflight observations can drift.
- TDD: retain original reproductions, run focused checks before/after each fix.
- Release targets are established by ARCHITECTURE_RELEASE_PLAN.md. Publication,
  current-link switch and launchctl restart are serial operations owned by main;
  prior independent reviews are complete, so there is no useful parallel write.
- A7: pre/post adapter race tests green with isolated tmux outside sandbox.
  Added independent UTF-8 tail-budget and exact SHA256 fixtures before extraction;
  moved them to public nativecapture boundary after extraction. No old cap raised.
- Read-only release discovery: origin https://github.com/Time4Mind/bria.git,
  main and remote refs/heads/main both 86510f12205d4ccebc4d000833dbe924e4ddc140.
  LaunchAgent gui/501/com.time4mind.bria.v2 running, PID 38207 at preflight.
  Executable /Users/a-s-nosko/.local/opt/bria-v2/current/bria; current symlink
  targets releases/20260903095611-auth-visible. Config ~/.bria-v2/config.json,
  state ~/.bria-v2/state.json. Settings revision and session created_at present;
  actual current-code compatibility passed using an isolated state copy. No secrets copied.
