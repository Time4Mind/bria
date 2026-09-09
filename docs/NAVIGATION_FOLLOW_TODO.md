# A29: navigation ownership, following pages and final card

## Immutable contract

Amendment 2026-09-09: owner explicitly states finals exceeding the history page
limit will not occur in his work. They are excluded from A29 acceptance; retain
current limits without a separate full-final mode. This resolves the prior
retention choice and releases the already authorized normal release sequence.
The known over-cap limitation is not fixed and must not be described as fixed.

Amendment 2026-09-09 A29.R1: owner explicitly reaffirms automatic push, merge and
deploy immediately after completed development and requests removal of rules
requiring repeat approval. This supersedes the mistaken prior approval pause.
Source: current user instruction; evidence: current repo, exact-SHA CI and live
local service. Unit: one completed Bria release; times in UTC/Moscow receipts.
Allowed changes: AGENTS release section, matching policy/test checks, this todo
and status pointer; normal task-branch push/merge or direct main push, same exact
local install/service targets below. No force/protection bypass, config/state/
secrets edits, deletion, provider prompts or outgoing user messages. Acceptance:
machine policy rejects removal of automatic release rule; full gate, exact main
SHA CI, installed artifact/process/lock and data-preservation postflight pass.
Coverage: main owns AGENTS/docs, integration and serialized Git/deploy; Carver
owns scripts/check_repo.go and scripts/check_repo_test.go only (RED/GREEN policy
coverage); Bernoulli read-only owns fresh service/lock/log/state preflight. Stop
at bounded proof or concrete blocker; no overlapping writers or broad searches.

Source: Artem's next request after A26-A28 release, 2026-09-09 Europe/Moscow.
Current source is this repository only, baseline main 1d0d77a. No legacy reads.
Result: background updates of a working session never overwrite Nodes, menu or
another view the user navigated to. While viewing the latest session page,
follow newly added pages until completion. While viewing an earlier page,
preserve the selected page during generation. In either case publish final
response in a NEW card/message, retaining the previous message; the new card
opens the page containing the START of the final answer, not its final page.
Navigation away must not be undone by stale queued/in-flight session edits.
Final notification is a distinct message, not a rewrite of the chosen menu.
Preserve persisted page plans, exact completion/retry dedup, Rich screenshot
cadence, cancellation, acceptance recovery and user data. No model replay.

Unit of acceptance: navigation plus in-flight/background delivery sequence for
one owner/chat, growing multi-page transcript and short/long final responses.
No analytical population/time denominator; times only matter in race ordering.
Allowed writes: bounded repo implementation/tests/docs, then existing standing
release scope after all obligations pass: normal origin/main push, exact-SHA CI,
verified trio install and restart only local gui/501/com.time4mind.bria.v2.
No live messages, provider prompts, manual state/config/secrets or destructive
writes. Validate external payloads with real consumers and fake HTTP; production
postflight checks version/process/lock/logs/getMe/data preservation, no claim of
manual visual Telegram testing. Source of current state must be reread live.

## Coverage map before parallel diagnosis

- Main: contract/docs, synthesis, exact ownership assignment, final full checks,
  serial Git and deployment. No agent mutations outside assigned files.
- Darwin: read-only navigation visibility/cancellation/queued edit path; identify
  smallest public failing seam for Nodes/menu/other views. No code edits yet.
- Anscombe: read-only persisted page selection/follow-on-growth path; last page
  vs historical page and restoration semantics. No code edits yet.
- Peirce: read-only completion new-card delivery and first-final-page selection,
  including exact-once retry/restart. No code edits yet.
- Bernoulli: read-only bounded recent live logs (10 minutes UTC), safe classes
  and ordering evidence; inspect no user payload/secrets, do not send messages.
- Carver: independent read-only contract/diff review once edits are integrated.

Stop each diagnosis at cause/proof or falsifiable hypothesis, exact files and
public regression seam. Budget: bounded source search per zone, no broad history
or external source fan-out. Main resolves overlap before any implementation.

## Obligations

Implementation ownership after independent diagnosis: Darwin owns both
telegrampromptcomposition and telegramcompletioncomposition (all files/tests),
plus native_visibility.go only if required. Anscombe owns controller.go and
new controller page-selection helpers/tests (not native_visibility.go).
Peirce owns cardtranscript and telegramui typed final-start metadata/projection,
plus telegramcontrolport DTO only if required. Main owns metadata conversion
wiring outside those zones after agreed API, cmd integration acceptance and docs.
Bernoulli completes read-only logs then may own one separately named cmd test
after assignment. Carver read-only independent reviewer. No package cap increase.

Confirmed static causes: prompt-status delivery bypasses view visibility guard;
commentary checks active session identity rather than actual view; semanticCard
does not follow growth when remembered page number remains valid; final UI
explicitly navigates to tail. New final carrier already exists and must be
preserved, not reimplemented. Actual HTTP requests accepted remotely cannot be
undone by context cancellation; inspect both ordering and final visible result.
Fresh logs had no recent relevant events and cannot prove or refute these races.
Bernoulli now owns cmd/bria/navigation_follow_integration_test.go only for real
persisted controller/flow/deliverer/HTTP acceptance, no production edits.
Agreed metadata: cardtranscript.Page and telegramui.ContentPage FinalStart bool;
Peirce sets typed markers and final selection, main wires runtimecomposition,
Darwin wires conversions in his two composition packages. No text heuristics.
Main also owns cardhistory/recovery.go and final_start_test.go: exact nonempty
same-turn adjacent final records from RestoreFinal are continuation fragments,
not separate answer beginnings. Peirce owns Block.FinalContinuation propagation.
Main public RestoreFinal -> Blocks -> RenderBlocks -> Paginate regression RED:
continuation advertised as answer beginning before the custody-metadata fix.
Architecture correction: Anscombe's initial page helper depended on UI/state;
main rejected it before integration. Approved helper is transport-neutral
cardpageselection with View, Reflow/Navigate over anchors and optional primitive
LoadCardPage getter. Main owns storage/card_page.go and its getter tests plus
registry; Peirce owns telegramui/navigation.go wrappers to the same resolver.
Controller cannot reach forbidden Telegram UI/state via a helper. Existing
package caps remain; new neutral package gets its own bounded registry entry.

Retention choice asked asynchronously: a protocol-sized final containing many
small fenced blocks can exceed chosen 32/64/128-page history cap and lose its
start. Proposed final-only limit up to existing hard 512-page bound; ordinary
history remains capped. No retention-policy change until user chooses; continue
independent navigation/follow/normal-final tests. Preserve this open question.

| ID | Acceptance | Status | Evidence |
|---|---|---|---|
| A29.1 | Navigation never overwritten by stale/background session rendering | locally verified | Joined menu/Nodes/scheduler/HTTP RED->GREEN; shutdown stays Unknown, navigation suppressed; full gate PASS |
| A29.2 | Latest page follows growth; historical page remains stable | locally verified | Public growth/pinned/reopen/shared resolver checks and full gate PASS |
| A29.3 | In-scope final new card, old retained, new selects start | locally verified | Short/long/pinned/split/A+B/reopen/late navigation/recovery PASS; owner excludes over-cap finals |
| A29.4 | Public RED/GREEN, race/integration/full gate and review | current code verified | Fresh full check-full PASS04:46UTC 2026-09-09; physical trio and independent review PASS |
| A29.R | Exact pushed SHA CI green, installed/restarted and postflight | release resumed | ed0fdbc pushed and both CI success; owner reaffirmed automatic release, new rules gate and deployment remain |
| A29.R1 | Automatic push/merge/deploy requires no repeated approval | locally verified | AGENTS and machine protection updated together; removal/old-conflict RED->GREEN, full gate PASS |
| A29.R2 | Resolve exact CI failure before release | locally verified, new CI pending | Controlled scheduling delay reproduces false negative control; deterministic sample oracle, race20 and full gate PASS; no runtime edit |

Initial hypotheses: (1) navigation does not invalidate pending view generation;
(2) already waiting transport mutations survive invalidation; (3) page follow
intent is inferred after page count changed; (4) completion reuses old carrier
or selects Latest instead of the final-start anchor. Discriminate via public
barrier-controlled ingress/send and persisted page-plan probes before fixes.

## Current checks

Main: RestoreFinal -> typed projection regression RED (second chunk falsely
marked beginning), GREEN after exact same-turn continuation metadata; combined
cardhistory/cardtranscript/UI race PASS. Neutral storage LoadCardPage RED on
real disk reopen with missing read seam, GREEN race 0.347s. Registry test RED
then GREEN for bounded neutral helper, retaining forbidden UI/state dependencies.
Joined real controller/store/flow/deliverers/Rich HTTP: owner's four navigation
cases and long-final selection RED, then GREEN. Main independent joined plus
restored split-final race PASS 3.125s. No live user message was sent.
Pure final metadata tests PASS but expose page-cap loss (including protocol-sized
40/600-fence examples); never relabel a retained continuation as the true start.
These scoped checks do not constitute the fresh full gate or authorize release
while the retention requirement remains unresolved.

Late integration RED: TestPendingRefreshCannotPublishFinalOnPreviousCard appends
the persisted final then releases a pending prompt-status refresh before final
delivery; actual Rich HTTP contains final on the OLD carrier. Dedicated final
send alone is insufficient. Main asks independent minimal producer/delivery
ordering fix; this public store/consumer test bypasses the producer so any
producer-scoped solution also needs an actual final-persistence event seam.
No release with this known violation. Existing passing receipts remain scoped.

### Final publication ordering fix (same A29.3, not a new product rule)

Main chooses durable/restart-safe ordering, not RAM-only pause. A card records
PendingFinalOperations (exact messageID + ':final') atomically with a newly
restored/persisted final; duplicate restoration must not re-arm a delivered final.
Routine projection suppresses while pending and rejects stale carrier snapshots.
Only final delivery's committed NEW carrier clears its own exact operation;
error/unknown/A's receipt cannot clear B. Controller cancels outstanding routine
view contexts around the physical final write; durable pending state guards new
contexts afterwards and after restart. Navigation never becomes visible due to
final confirmation. Existing schema-compatible optional field, no manual state
edit and no new transport sender. Final cap decision remains independently open.

Disjoint handoff: Bernoulli now owns telegramstate/state.go and tests plus
cardhistory/recovery.go and tests (main's prior edits are frozen, preserve them)
for pending-final persistence/cloning/validation. Darwin owns both routine/final
composition packages and cmd/navigation_visibility_test.go for pending guards,
carrier rechecks and setting CardOutput.FinalOperationID only for Final.
Anscombe owns controller.go/native_visibility.go/technical_history.go and a new
cmd/final_publication_order_test.go for producer write-window cancellation with
public real final-save/retry evidence. Main owns flow CardOutput/commit atomic
pending clearing, its tests, synthesis/docs and any registry. Peirce frozen.
All previous page-selection/navigation edits remain and are checked together.

### Integration follow-up

Added exact FinalOperationID to typed blocks/pages and completion target so an
older queued final A cannot publish newer B. Main actual disk RestoreAcceptedFinal
-> Rich HTTP tests now PASS for split 20KiB final, suppressed pending routine
refresh and two pending finals selecting their own start/clearing only their
own fence (race 2.120s). The first real-disk check exposed non-nil empty pending
slice vs omitempty reread mismatch; state owner canonicalizes cleared custody.
The earlier A/B fixture accidentally fit one page and was corrected to a real
multi-page answer; that fixture failure is not product RED evidence.

Darwin additionally owns new carddeliveryguard: routine visible-view context,
durable pending/carrier rechecks and exact-final target selection, no projection
or sending. Main registered it with only domain/telegramstate imports, cap100;
both compositions use it without raising existing caps. Current architecture,
architecture tests and format PASS; later edits still require rerun.

Retention remains open: 512 pages is not a guarantee (600 short fences fit the
32KiB protocol bound). Asked refined choice: separate full-final storage/display
independent of ordinary history cap, or retain common cap. No answer received
and no retention-policy change performed. Full task/release cannot be marked
complete merely because ordinary-sized finals pass.

### Independent review: further ordering boundaries (open until verified)

Carver identified three required regressions in the new fence; not unrelated
backlog. (1) Native accepted-final recovery writes pending but currently emits
only PromptStatus, so publication must rejoin the existing durable final output.
Bernoulli diagnoses exact recovery/dispatcher wiring before bounded ownership.
(2) Final A prepared active before user selects B must not set ActiveSession=A
on late receipt. Main public flow regression RED then GREEN race0.360s after
final commits cease changing active selection; Peirce owns only new
cmd/bria/late_final_navigation_test.go for real select/delivery/refresh acceptance.
(3) Controller shutdown with a live delivery parent must not be acknowledged as
navigation suppression. Anscombe owns distinct context cancellation cause in
controller/native_visibility; Darwin owns guard classification and public HTTP
barrier test. Existing scopes are otherwise frozen. No release until all three
are independently checked along with the unresolved retention decision.

Combined changed-package race: eleven component packages PASS. cmd/bria failed
producer test's other-session wire expectation; owner inspecting actual method
before changing assertion. Do not describe this run as whole-suite PASS.

Recovery implementation ownership now approved: Bernoulli owns new
durablecomposition/recovered_final.go and tests plus cmd/recovered_final_output_test.go.
Decorator writes retained history then accepts exact final through existing
OutputCustody before reconciliation may complete input. Main wired only the
production FinalRestorer in singlemachinecomposition/run.go; no new sender.

Review also confirmed normal producer notifies before OnCompleted, but swallowed
enqueue errors could still orphan pending. Main now owns controller.go and new
telegramcontroller/final_custody_test.go; notify returns exact custody success,
final without durable receipt returns accepted AwaitingRecovery, not Succeeded.
Public RED (succeeded despite error) -> GREEN for storage error/wrong receipt,
next root deferred, no provider replay; adjacent retry/custody race PASS1.422s.
Anscombe retains native_visibility.go and producer test only for shutdown cause;
Darwin owns its classification. A shutdown context must not pretend visibility.
No production acceptance claimed before joined reconciliation/reopen proof.

Architecture gate then found controller5518 lines > existing5500; no cap increase.
Anscombe owns new stdlib-only viewdeliverycontext (cap75) and native_visibility
wrapper: child delivery cancellation and distinct service-stop cause are one
small responsibility, separate from session/UI state. Main owns registry/tests.
Closed controller remains not visible and returns an already canceled delivery
context; Darwin checks that error before interpreting hidden view as suppression.
This extraction must pass the existing producer/navigation/shutdown probes.

### Frozen integration before full gate

All owners frozen. Main fresh architecture/scripts/format PASS. Main targeted
race: viewdeliverycontext0.312s, durablecomposition0.543s, controller4.450s,
guard0.615s, cmd5.471s; prompt/completion compiled but no tests matched that
particular filter, their prior full-package race and upcoming whole gate cover
them. Main restored/split/pending/A+B/late-selected-B Rich wire race x3 PASS5.559s.
Late final low-level exact custody/selection race x10 PASS0.325s.
Anscombe reports late-view race RED7/20 then GREEN50/50; physical-write/retry/
menu/other-session/shutdown joined race x3 PASS11.031s. Controller5492/5500,
view helper60/75, guard92/100, completion275/275; existing caps not raised.

Independent review closes the three additional ordering defects in current
source, subject to main full gate. Bernoulli actual normal producer -> failed
enqueue -> retained Accepted -> reopen -> recovery -> ordinary output dispatcher
PASS. Ambiguous after-write enqueue is checked separately with real journal and
reconciler/reopen; no claim of a single combined producer+ambiguous+HTTP scenario.
No external HTTP sent: all wire tests use injected fake HTTP. Final retention
contract remains unresolved, so fresh local checks alone cannot authorize release.

Full command in progress:
`env TMPDIR=/private/tmp GOMODCACHE=/Users/a-s-nosko/go/pkg/mod VERSION=20260909-navigation-follow make check-full`.

First full gate stopped at an old spacing fixture: final metadata belongs to
prompt-1, but completion-send used unrelated spacing:completion operation. Exact
final selection correctly rejects that mismatch. Main owns one-line fixture
correction in internal/integration/card_spacing_acceptance_test.go to the actual
prompt-1:final operation; spacing/table/Rich assertions remain unchanged. This
is test contract migration, not evidence of a new production bug. Rerun its
targeted race then the whole gate; first run is not a passing release receipt.

## Current handoff and acceptance receipt

2026-09-08 23:46 UTC (2026-09-09 02:46 Europe/Moscow): targeted spacing wire
race PASS1.427s, then second full `make check-full` PASS. Includes policy/source
secret scan, architecture+registry tests, formatting, all plain tests, vet,
operational packaging/update contracts, all race tests and actual executable
trio acceptance. Current source worktree tested; no Git commit or CI claim.
Selected fresh full-race timings: cmd83.310s, integration17.444s,
durablecomposition11.245s, controller9.061s, storage52.690s, flow27.752s,
viewdeliverycontext0.536s, scripts39.931s. Unchanged packages may use Go cache.

Physical artifacts: `bin/bria --version` -> `bria 20260909-navigation-follow`;
all three binaries are Mach-O arm64. SHA256:

- bria: b540e9bd4b8f71086635ba240f2006105826f330df1763c82137dcf7ba2660a0
- bria-codex-adapter: 792059a14d750aae8367d7b23d77434e0bc3cd604d01e7040e1f9800d4f41911
- bria-claude-adapter: 88d612486c8c3171489dd369673a2e93e05f6b84aa97092bc2403af850648263

Read-only service check after build: gui/501/com.time4mind.bria.v2 running,
PID90706/runs1/never exited, current remains
20260908-separate-spoiler-limits. No A29 push, install, restart, live user message,
provider prompt or manual state/config changes. No visual Telegram acceptance.
All agents frozen/closed; main owns synthesis and continuation.

Remaining A29.3: await owner decision on full final display independent of
ordinary history page cap. No response to the async choice yet. Existing cap
cannot guarantee the final start; neither 512 pages nor simple packing proves
all protocol-sized cases. Do not mark A29 complete or release this partial build.
Next: after choice, amend the contract for all new work, implement/verify exact
long-final semantics and rerun full gate. Then standing release authorization
applies automatically to the already bounded repo/service targets; no additional
release request is needed. A26-A28 remain released and are not reopened.

## Release continuation after owner exclusion

A29.R2 bounded CI-fix iteration, same authorized release contract: main owns
cmd/bria-claude-adapter/nested_heartbeat_test.go and docs; Carver independently
reviews the test-only diagnosis/fix. No runtime/provider changes or new targets.
Exact failure: 8e9a017 Stage1 run34314357545, 2026-09-09 05:20:18 UTC,
TestNestedHeartbeatLivePublisherNeverPassesQuiescence got nil at155.597154ms
instead of200ms timeout. Matrix run34314357568 succeeded. Ranked hypotheses:
(1) publisher scheduling pause exceeds100ms observation window - reproduce with
controlled writer delay; (2) malformed atomic counter - predicts read error,
not observed nil; (3) real adapter Abort - absent in this test (in-process Go
publisher only); (4) deadline selection - nil before200ms instead matches quiet
threshold, inspect using the controlled probe. Acceptance: reproduced false
negative-control assumption, deterministic advancing-sample oracle and original
actual subprocess abort test preserved, stress/race/full gate/new exact-SHA CI.

Controlled 250ms publisher delay reproduced nil at100.461125ms; temporary delay
removed. The old test assumed a live goroutine guarantees file writes every5ms,
which is not a scheduling guarantee. Exact GitHub scheduler delay was not traced;
the invalid test assumption is independently reproduced. Replaced only its oracle:
each sample publishes/reads an advancing counter through actual atomic file IO;
the path wrapper and real subprocess Abort test remain intact. Test name now
states advancing samples, not guaranteed live-process liveness. No production
change. Full adapter race count20 PASS12.933s; fresh make check-full PASS
2026-09-09 05:24 UTC and all three binary hashes unchanged. Follow-up manifest:
cmd/bria-claude-adapter/nested_heartbeat_test.go, this todo and status pointer.

A29.R1 exact follow-up manifest: AGENTS.md, scripts/check_repo.go,
scripts/check_repo_test.go, docs/NAVIGATION_FOLLOW_TODO.md,
docs/STATUS_AND_NEXT.md, scripts/standing_release_policy_test.go (main-owned
two-line migration of existing fixture to amended wording, same safeguards).
Current branch is main and GitHub has no open PR;
perform direct normal push, no artificial merge. Read-only instruction review
approves automatic future releases with the same safety/target boundaries.
Preflight 2026-09-09 05:13:25 UTC: PID90706 running/sole lock; 5 sessions
(ready3/archived2), 5 cards/146 history, 16 completed inputs, no pending finals
or input/output leases. Four old unknown outputs unchanged. Safe ten-minute log
window has zero new records/errors; no broader absence-of-errors claim.

A29.R1 checks: seven removal mutations RED then GREEN, two restorations of the
old unconditional deploy-consent rule RED (errors=[]) then GREEN. Main migrated
two wording assertions in existing standing-release fixture without removing
their safeguards. All TestPolicyChecker race tests PASS5.619s. Fresh full
make check-full PASS 2026-09-09 05:18 UTC (policy/architecture/plain/vet/packaging/
race/build and executable trio); scripts race7.903s, integration race8.345s,
unchanged packages may use Go cache. No runtime source changed after ed0fdbc.

Latest receipt: source ed0fdbced60f50f0a62232c17599888789820eee is pushed to
origin/main. Exact-SHA GitHub runs 34312399038 (Platform build matrix) and
34312399039 (Stage 1 checks) both completed successfully. The independent helper
review approves the healthy/quiescent path after ambiguity/recovery guards.
No install/restart had occurred at that receipt. Owner has now explicitly
reaffirmed automatic release and removed the repeat-confirmation requirement.
Next authorized sequence after rules checks/push/CI: install the verified trio to the
exact directory below, switch existing current, restart only the named local
service, then verify hashes/version/process/lock/logs/getMe/data preservation.
Before stopping, repeat quiescence and identity checks because the user can act
while release checks run. Four pre-existing unknown output deliveries remain
untouched; no resend or model replay is authorized.

User: "(таких кейсов не будет в моей работе)". Current acceptance excludes final
answers beyond the configured history page cap, keeps that setting unchanged,
and retains the documented limitation. Earlier waiting-for-choice notes above
are historical, not current blockers. No new feature or retention write.

Coverage: main owns docs, full check, bounded install helper and all Git/deploy
writes. Carver read-only verifies amended spec vs A29 diff at baseline1d0d77a,
no new broad review or edits. Bernoulli read-only verifies existing local service,
paths/process/lock and safe state/log counts, no raw payload/secrets. Separate
zones, no file write overlap. Stop at concrete release blocker or evidence;
one bounded current-state probe per zone, no model/Telegram messages.

Bounded release targets: origin/main https://github.com/Time4Mind/bria.git;
bin trio version20260909-navigation-follow -> new local release directory
/Users/a-s-nosko/.local/opt/bria-v2/releases/20260909-navigation-follow;
existing current symlink and gui/501/com.time4mind.bria.v2, same existing plist.
Sequence: full gate -> exact manifest commit -> normal push -> both exact-SHA
CI green -> candidate config check -> preserve state/settings/journal -> guarded
stop/symlink/bootstrap -> reread version/hashes/process/lock/getMe/safe logs and
preservation. No force, config/state edits, secrets, outgoing user messages,
other services/hosts or deletion. Terminal criterion is verified deployed state,
not merely an installed file. Existing previous release directory is retained;
do not invoke the stale A26 install helper whose targets/PID no longer apply.

Fresh full gate PASS 2026-09-09 04:46 UTC; rebuilt binary hashes match the
23:46 receipt exactly. Read-only amended-spec review approves, no in-scope blocker.
The previous GitHub CI success belongs to1d0d77a, not yet to A29.

Exact commit manifest (57 files; ignored local install helper excluded):

```text
cmd/bria/final_card_delivery_retry_test.go
cmd/bria/final_publication_order_test.go
cmd/bria/late_final_navigation_test.go
cmd/bria/navigation_follow_integration_test.go
cmd/bria/navigation_visibility_test.go
cmd/bria/page_selection_test.go
cmd/bria/recovered_final_output_test.go
cmd/bria/restored_final_card_test.go
docs/NAVIGATION_FOLLOW_TODO.md
docs/STATUS_AND_NEXT.md
internal/carddeliveryguard/guard.go
internal/carddeliveryguard/guard_test.go
internal/cardhistory/final_start_test.go
internal/cardhistory/pending_final_test.go
internal/cardhistory/recovery.go
internal/cardhistory/recovery_test.go
internal/cardpageselection/navigation.go
internal/cardpageselection/navigation_test.go
internal/cardpageselection/selection.go
internal/cardpageselection/selection_test.go
internal/cardtranscript/final_identity_test.go
internal/cardtranscript/final_start_test.go
internal/cardtranscript/pages.go
internal/cardtranscript/pages_test.go
internal/cardtranscript/transcript.go
internal/durablecomposition/recovered_final.go
internal/durablecomposition/recovered_final_test.go
internal/integration/card_spacing_acceptance_test.go
internal/singlemachinecomposition/run.go
internal/storage/card_page.go
internal/storage/card_page_test.go
internal/telegramcompletioncomposition/composition.go
internal/telegramcompletioncomposition/final_operation_test.go
internal/telegramcompletioncomposition/question_policy.go
internal/telegramcontroller/controller.go
internal/telegramcontroller/final_custody_test.go
internal/telegramcontroller/native_visibility.go
internal/telegramcontroller/technical_history.go
internal/telegramflow/final_fence_test.go
internal/telegramflow/flow.go
internal/telegrampromptcomposition/composition.go
internal/telegrampromptcomposition/composition_test.go
internal/telegrampromptcomposition/native.go
internal/telegrampromptcomposition/voice_lifecycle_spacing_test.go
internal/telegramruntimecomposition/composition.go
internal/telegramstate/pending_final_test.go
internal/telegramstate/state.go
internal/telegramui/carrier.go
internal/telegramui/final_identity_test.go
internal/telegramui/final_start_test.go
internal/telegramui/navigation.go
internal/telegramui/pagination.go
internal/viewdeliverycontext/context.go
internal/viewdeliverycontext/context_test.go
scripts/card_page_boundary_test.go
scripts/check_repo.go
scripts/check_repo_test.go
```
