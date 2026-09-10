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

Task amendment A36: before the pending release, Artem also requires the Nodes and
Status screens to expose `Назад`, not `Меню`, and the currently active session
card to use the clearer formatting demonstrated by historical `bria-legacy` and
ccbot. Evidence sources are the current repository, a bounded read-only projection
of the current active card/state and safe service logs, plus historical formatting
code only in the two explicitly requested projects. Unit: projected Telegram
button and one rendered active-card page. Population: Nodes, Status, and all
session-card rich-Markdown blocks; exclusions: other menu labels, semantic content,
pagination rules, provider output, config/state and manual Telegram input.

A36 coverage: the integration owner owns the task contract, current-code tests,
fix, synthesis and release. A read-only active-card investigator owns current
state/log/render evidence. A read-only historical investigator owns the
`bria-legacy`/ccbot comparison. They do not edit files. Stop when the visible
defect is reproduced at a public projection/render seam, the requested historical
behavior is isolated without importing legacy state, focused RED/GREEN and race
tests pass, and the combined exact release completes the full and live gates.

Task amendment A37: Artem's live screenshot of the same active card after the
A36 release shows ordinary Markdown quote markers (`>`, `> >`, `> -`) rendered
as visible prose. Source evidence is that screenshot, the persisted final entry
and the exact Rich wire path. Unit: one contiguous Markdown blockquote in any
old or new session-card entry. Population: raw and HTML-escaped quote markers
outside literal code; exclusions: comparison operators, code spans/fences,
technical spoilers and semantic content. Allowed writes and release targets are
unchanged. Acceptance: the screenshot-shaped public wire test is RED before the
fix and emits one native `<blockquote>` after it, old persisted history remains
unchanged and becomes correct on ordinary card re-render, full gate and release
postflight pass.

| ID | Required outcome | Status | Evidence / open boundary |
| --- | --- | --- | --- |
| A35.1 | Identify the latest individual Status callback chains | verified | Two double-click groups: 17:50:25/27 UTC and 17:57:03/08 UTC; every callback/edit completed without an error |
| A35.2 | Determine when provider quota is fetched and committed | verified | `SemanticRefreshStatus` starts `Refresh` in a goroutine, immediately reads `Snapshots`, and renders the pre-refresh cache; refresh completion updates only the in-memory cache |
| A35.3 | Decide whether first-click stale/second-click fresh is real | verified | First controller responses took 7-9 ms and Telegram edits 162-174 ms, before a bounded provider CLI poll can complete; no completion-driven edit exists, so a later click is required to display that result |
| A35.4 | Implement completion-driven refresh with stale-navigation fencing | verified locally | Cached edit commits before one background poll; completion conditionally edits the exact current signed global presentation, and repeated taps retarget the same poll |
| A35.5 | Add safe refresh lifecycle timing | verified locally | `quota.refresh` emits start and completed/skipped/failed timing through the payload-free Telegram trace; no quota values or provider payload are present |
| A35.6 | Full verification and automatic release | verified through source CI | Public RED/GREEN and full gate passed; `d3ce82b` passed Stage 1 `34403466785` and Platform matrix `34403466737`; installation was deliberately deferred when A36 enlarged the same release |
| A35.7 | Fix mandatory macOS CI readiness/abort race | verified in exact-SHA CI | Exact-record readiness fence passed 10,000 stress and 2,500 race interleavings plus all eight platform jobs in run `34403466737` |
| A36.1 | Nodes and Status show `Назад`, not `Меню` | verified locally | Public signed-button RED produced `≡ Меню`; preserving the semantic label through projection/presentation is GREEN and keeps the authenticated action unchanged |
| A36.2 | Diagnose the malformed current active-card formatting | verified | Active `workdir6` card was on page 53/54 inside technical history; bounded neighboring metadata contained 10-16-line `exec` commands rendered as undifferentiated spoiler text |
| A36.3 | Match the useful historical formatting without legacy-state coupling | verified locally | Historical legacy/ccbot comparison isolated native Rich code blocks as the useful difference; current spacing is already equivalent and remains unchanged |
| A36.4 | Verify and release the combined A35+A36 version | verified | Commit `7ce4578` passed Stage 1 `34408284521` and Platform matrix `34408284601`; `20260910-status-formatting` is installed with matching trio hashes, PID 51199 running as sole lock holder, and preserved state/settings/config |
| A37.1 | Reproduce the live blockquote defect from the active card | verified | Persisted final entry 215 retains 23 newlines and raw `>` markers; public Rich-wire RED sent the corresponding `&gt;` lines unchanged, matching the screenshot |
| A37.2 | Render ordinary Markdown quotes natively without touching literals | verified locally | Consecutive raw or escaped quote lines become one `<blockquote>`; blank quote lines and lists are preserved, fenced markers stay code, and normalization is idempotent |
| A37.3 | Prove old-session applicability | verified locally | The fix runs after persisted history is projected, not during ingestion; entry 215 needs no migration and will use the new renderer on its next ordinary page render |
| A37.4 | Full verification and automatic follow-up release | in progress | Focused public Telegram/Rich/card tests pass; race, full gate, Git/CI/install/restart/postflight remain |

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
the CI-fix. Follow-up `d3ce82b` passed Stage 1 run `34403466785` and all eight
Platform matrix jobs in run `34403466737`. Installation was deferred because
Artem added A36 before the pending restart, so the combined version is released
once rather than briefly installing the superseded A35-only candidate.

## A36 diagnosis and implementation receipt

The Nodes/Status controller already emitted `Назад`, but semantic projection
dropped that label and callback presentation substituted its global fallback
`≡ Меню`. The public signed-presenter regression failed with that exact visible
label before the fix and now preserves `Назад`; empty or explicitly menu-labelled
uses retain the existing fallback behavior.

A bounded read of the current active card identified `workdir6`, page 53 of 54,
anchored in technical history. Neighboring entries were completed `exec` calls
with 10-16 command lines. Current rendering placed those arguments in ordinary
spoiler text. The explicitly requested historical comparison showed both
`bria-legacy` and ccbot using native Rich code blocks for commands, while their
inter-event spacing matches the current NBSP separator. No legacy state or
unrelated renderer was imported.

The current renderer now places `exec`/Bash/shell arguments in a language-shell
code fence, keeps output in a separate escaped block, chooses a fence longer than
embedded backticks, and preserves balanced wrappers while paginating. The central
Rich boundary converts fenced code to Telegram-native `<pre><code>`; tables,
independent command/output line limits and saved notification pages remain intact.
RED/GREEN covered command formatting, nested fences, Rich conversion and the
signed back label. Focused affected-package race tests passed ten repetitions and
the complete `VERSION=20260910-status-formatting make check-full` passed. No
state, settings, config, Telegram input or live message was modified during
diagnosis and local verification.

## Combined release receipt

Commit `7ce4578d12255b2b082fcfa5c336185a656b9f90` is the exact source used for
the installed executable trio. Stage 1 run `34408284521` and Platform matrix
run `34408284601` passed. The active release is
`20260910-status-formatting`; `previous` points to
`20260909-approval-restart-stall`. Installed SHA-256 values are
`d122410e...38383` for Bria, `f9e24dbe...a46ec` for the Codex adapter and
`54203c53...cdab3` for the Claude adapter.

Postflight at 2026-09-10 00:50 Europe/Moscow found launchd state `running`,
PID 51199 and exactly that process holding `.state.json.lock`. Seven sessions,
seven cards and 553 card-history entries were preserved; the message journal
remained at 12 sessions and 26 inputs and gained one startup/recovery output.
Config and settings hashes remained unchanged. The structured startup window
contains `telegram.flow_ready`, one recovered live session and no critical
event. The visible Telegram rendering and the one-click Status update still
require the owner's next ordinary interaction for live acceptance; no manual
Telegram input was sent during deployment.
