# Status refresh diagnosis

Task contract: 2026-09-09, Europe/Moscow. Determine whether one press of the
Telegram Status control shows a stale provider quota and a second press is
required to display the current value. Source evidence is the current installed
Bria structured log, current repository code and bounded read-only provider
probes when needed. Unit: one Status callback and its quota-read, render,
transport-edit and durable-receipt chain. Population: the owner's latest Status
interactions for the current Bria release. Exclusions: manual session input,
config/state mutation and unrelated card/session behavior.

Allowed writes after Artem's explicit implementation authorization are this
repository, normal commit/push/CI, and the standing-authorized release/restart of
`gui/501/com.time4mind.bria.v2`. State, config, secrets, manual Telegram input and
other services remain excluded. Acceptance: public RED/GREEN behavior, focused
race suites, full versioned `make check-full`, exact manifest/commit/push, green
required CI for `origin/main`, matching installed artifacts, healthy sole
service/lock holder, preserved sessions/cards/settings, and a live one-click
Status refresh without a second user action.

Coverage map: integration owner owns `internal/telegramcontroller`, composition,
tests, synthesis and release. Maxwell performs read-only discovery of an existing
background-completion-to-card seam. Curie performs read-only concurrency and
navigation risk review. Faraday performs read-only safe-logging boundary review.
Their scopes do not overlap and no agent writes. Stop when the public test proves
cached immediate receipt plus automatic fresh edit, stale navigation is fenced,
single-flight is retained, safe timing is observable, and full/live gates pass.

| ID | Required outcome | Status | Evidence / open boundary |
| --- | --- | --- | --- |
| A35.1 | Identify the latest individual Status callback chains | verified | Two double-click groups: 17:50:25/27 UTC and 17:57:03/08 UTC; every callback/edit completed without an error |
| A35.2 | Determine when provider quota is fetched and committed | verified | `SemanticRefreshStatus` starts `Refresh` in a goroutine, immediately reads `Snapshots`, and renders the pre-refresh cache; refresh completion updates only the in-memory cache |
| A35.3 | Decide whether first-click stale/second-click fresh is real | verified | First controller responses took 7-9 ms and Telegram edits 162-174 ms, before a bounded provider CLI poll can complete; no completion-driven edit exists, so a later click is required to display that result |
| A35.4 | Implement completion-driven refresh with stale-navigation fencing | verified locally | Cached edit commits before one background poll; completion conditionally edits the exact current signed global presentation, and repeated taps retarget the same poll |
| A35.5 | Add safe refresh lifecycle timing | verified locally | `quota.refresh` emits start and completed/skipped/failed timing through the payload-free Telegram trace; no quota values or provider payload are present |
| A35.6 | Full verification and automatic release | in progress | Public RED failed on missing auto-edit seam; GREEN, focused 10x race and final versioned `make check-full` pass; Git/CI/install/restart/live acceptance remain |
| A35.7 | Fix mandatory macOS CI readiness/abort race | verified locally | Exact-record readiness fence added; 10,000 stress interleavings, 2,500 race-detector interleavings and a fresh full `make check-full` pass |

Ranked hypotheses: (1) the callback renders before an asynchronous quota refresh
updates the cache; (2) the first transport edit completes late and is mistaken for
the second click; (3) a cache TTL intentionally suppresses the first provider read;
(4) the provider value changes between clicks; (5) no defect is present and the
observation is a timing coincidence. The cheapest discriminator is the existing
safe callback/quota/transport timeline; no new instrumentation is authorized yet.

## Diagnosis receipt

The symptom is deterministic in the current implementation. In
`statusSemanticResultWithRefresh`, `startQuotaRefresh` launches
`providerquota.Service.Refresh` asynchronously and then `Snapshots` is read
immediately. `Refresh` eventually replaces the in-memory provider snapshots, but
there is no completion callback, notification or transport edit. The next Status
press therefore reads the refreshed cache produced by the previous press.

Safe detailed logs contain two recent pairs matching the report. At 17:50 UTC the
presses were 2.111 seconds apart; at 17:57 UTC they were 4.443 seconds apart. The
first callbacks rendered in 7-9 ms and reached Telegram in under 0.5 seconds, so
their output could not await the provider CLI collection. All observed callback,
transport and state-commit stages completed; this is not a failed Telegram edit.

The logs do not contain quota-refresh start/end, cache age or a value reference,
so they cannot prove which exact percentage changed. The code path proves the
stale-first/fresh-later ordering and exposes the observability gap. No product,
runtime, state, config, Telegram message or external provider was changed during
this diagnosis.

## Implementation receipt

The public RED tests failed because no current-global-surface editor or
post-commit refresh coordinator existed. The implementation now starts quota
collection only after the cached callback edit has a confirmed receipt and a
registered signed presentation. Completion renders the fresh snapshot and edits
the same carrier only if the exact presentation remains current; a newer menu,
session or Status presentation wins atomically. Repeated refresh commits update
the target while one provider collection remains in flight.

Focused GREEN covers post-commit ordering, signed fresh buttons, file-registry
readback, stale navigation, repeated taps and shutdown cancellation. Ten repeated
race runs pass for the affected flow/controller/composition packages. The package
architecture registry and its machine tests were expanded to describe the new
bounded responsibilities; the architecture gate passes.

The exact final source passed `VERSION=20260909-status-refresh make check-full`,
including all tests, all-package race detection, vet, policy, architecture,
packaging contracts and the executable-trio acceptance. The checked candidate
reports `bria 20260909-status-refresh`; release writes remain pending.

The first exact-SHA CI iteration passed Stage 1 run `34402279412` but macOS job
`102636797653` in platform run `34402279296` exposed an independent existing
readiness/abort race. `finalizeReap` can remove a just-exited process under
`Starter.mu`, unlock, and only then close `record.done`; in that gap `Start` saw
`done` open, returned a binding for the already-untracked record, and immediate
`Abort` returned `ErrSessionNotTracked`. The bounded CI-fix makes readiness commit
verify under the same lock that the exact process record is still tracked.
The existing public regression test then passed 10,000 interleavings normally
and 2,500 under the race detector; the complete versioned gate also passed with
the CI-fix. A follow-up commit and exact-SHA CI rerun remain before installation.
